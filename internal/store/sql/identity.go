//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// File identity in app.db (ADR-0031, migration 0002): the files, pending, tombstones and
// trash_records tables, and three meta rows. This file has the first assignment and the
// lookups; the scan's changes come with the scan.

// The meta rows of file identity.
const (
	vaultUUIDKey   = "vault_uuid"   // the vault's UUID, also in the vault marker
	assignedKey    = "ids_assigned" // the "assigned" flag: when the first assignment committed
	rebuildUUIDKey = "rebuild_uuid" // the db rebuild that made this database, if one did
)

var (
	// ErrAssigned: the database already has its first assignment. Nothing was changed.
	ErrAssigned = errors.New("sql: file IDs are already assigned in this database")
	// ErrBadUUID: a vault or rebuild UUID isn't a lower-case UUID. Nothing was changed.
	ErrBadUUID = errors.New("sql: not a UUID")
	// ErrNoFile: no live or suspect row has that path or ID.
	ErrNoFile = errors.New("sql: no file with that path or ID")
)

// uuidPattern is a UUID as PostelMD writes it: lower-case hex, 8-4-4-4-12.
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// FileStat is what a row keeps of a file's last stat: the signals of ADR-0031's scan and
// rename rules.
type FileStat struct {
	Size          int64
	Device, Inode uint64
	// MtimeNS and CtimeNS are nanoseconds since 1970.
	MtimeNS, CtimeNS int64
}

// NewFile is one file found by the first assignment's walk. SHA256 is nil when a busy
// writer kept it from being hashed (three tries; the scan finishes it).
type NewFile struct {
	Path   string // vault-relative, bytes as on disk
	SHA256 []byte
	Stat   FileStat
}

// TrashImport is one item in the vault's trash at the upgrade (ADR-0031's trash import).
type TrashImport struct {
	OpID      string // the trash entry's name: the stamp folder, or <stamp>-<name>
	Name      string // the item's name before the delete
	Kind      string // "file" or "folder"
	DeletedAt time.Time
}

// Assignment is the first assignment's one transaction (ADR-0030's upgrade step 3, and
// db rebuild's step 4): the vault UUID, every file row at generation 1, the trash records,
// the flag, and, for a rebuild, its UUID.
type Assignment struct {
	VaultUUID   string
	RebuildUUID string // "" unless a db rebuild assigns
	Files       []NewFile
	Trash       []TrashImport
	Now         time.Time // the IDs' time, the flag's, and the imported items' 7-day clock
}

// Assign commits a in one transaction: all of it, or nothing. A database that already has
// the flag is refused (ErrAssigned), as is a malformed UUID (ErrBadUUID); a row the schema
// refuses (two rows for one path, a hash that isn't 32 bytes, an unknown kind) fails the
// whole transaction.
func (db *DB) Assign(ctx context.Context, a Assignment) error {
	if !uuidPattern.MatchString(a.VaultUUID) || a.RebuildUUID != "" && !uuidPattern.MatchString(a.RebuildUUID) {
		return ErrBadUUID
	}
	err := db.writeTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM meta WHERE key = ?", assignedKey).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return ErrAssigned
		}
		if err := insertFiles(ctx, tx, a.Files, a.Now); err != nil {
			return err
		}
		if err := insertTrash(ctx, tx, a.Trash, a.Now); err != nil {
			return err
		}
		meta := map[string]string{vaultUUIDKey: a.VaultUUID, assignedKey: a.Now.UTC().Format(time.RFC3339Nano)}
		if a.RebuildUUID != "" {
			meta[rebuildUUIDKey] = a.RebuildUUID
		}
		for k, v := range meta {
			if _, err := tx.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES (?, ?)", k, v); err != nil {
				return fmt.Errorf("sql: record %s: %w", k, err)
			}
		}
		killPoint("assign-before-commit")
		return nil
	})
	if err != nil {
		return err
	}
	killPoint("assign-after-commit")
	return nil
}

func insertFiles(ctx context.Context, tx *sql.Tx, files []NewFile, now time.Time) error {
	st, err := tx.PrepareContext(ctx, `INSERT INTO files (id, path, generation, sha256, size, device, inode,
		mtime_ns, ctime_ns, state) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, 'live')`)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	for i, f := range files {
		var hash any // NULL, not an empty BLOB, for a file not hashed yet
		if f.SHA256 != nil {
			hash = f.SHA256
		}
		if _, err := st.ExecContext(ctx, NewID(now), []byte(f.Path), hash, f.Stat.Size, int64(f.Stat.Device), //nolint:gosec // G115: the bits are kept
			int64(f.Stat.Inode), f.Stat.MtimeNS, f.Stat.CtimeNS); err != nil { //nolint:gosec // G115: the bits are kept
			// Which row, by its place in the walk: never its path (CLAUDE.md rule 15).
			return fmt.Errorf("sql: record file %d of %d: %w", i+1, len(files), err)
		}
	}
	return nil
}

func insertTrash(ctx context.Context, tx *sql.Tx, items []TrashImport, now time.Time) error {
	for i, t := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO trash_records (op_id, name, original_path, kind, state,
			deleted_at_ms, clock_ms) VALUES (?, ?, NULL, ?, 'trashed', ?, ?)`,
			t.OpID, []byte(t.Name), t.Kind, t.DeletedAt.UnixMilli(), now.UnixMilli()); err != nil {
			return fmt.Errorf("sql: record trash item %d of %d: %w", i+1, len(items), err)
		}
	}
	return nil
}

// Identity is what the database says about the vault it describes.
type Identity struct {
	VaultUUID   string
	Assigned    bool   // the flag: the first assignment committed
	RebuildUUID string // set by a db rebuild
}

// Identity reads the vault UUID, the flag and the rebuild UUID (the last committed values).
func (db *DB) Identity(ctx context.Context) (Identity, error) {
	rows, err := db.r.QueryContext(ctx, "SELECT key, value FROM meta WHERE key IN (?, ?, ?)", vaultUUIDKey, assignedKey, rebuildUUIDKey)
	if err != nil {
		return Identity{}, fmt.Errorf("sql: read the vault identity: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var id Identity
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return Identity{}, fmt.Errorf("sql: read the vault identity: %w", err)
		}
		switch k {
		case vaultUUIDKey:
			id.VaultUUID = v
		case assignedKey:
			id.Assigned = true
		case rebuildUUIDKey:
			id.RebuildUUID = v
		}
	}
	if err := rows.Err(); err != nil {
		return Identity{}, fmt.Errorf("sql: read the vault identity: %w", err)
	}
	return id, nil
}

// File is a row of files.
type File struct {
	ID           string
	Path         string
	Generation   int64
	SHA256       []byte // nil until hashed
	Stat         FileStat
	Suspect      bool
	SuspectSince time.Time // zero unless Suspect
}

const fileColumns = "id, path, generation, sha256, size, device, inode, mtime_ns, ctime_ns, state, suspect_since_ms"

// FileByPath returns the row at path, or ErrNoFile.
func (db *DB) FileByPath(ctx context.Context, path string) (File, error) {
	return scanFile(db.r.QueryRowContext(ctx, "SELECT "+fileColumns+" FROM files WHERE path = ?", []byte(path)))
}

// FileByID returns the row with id, or ErrNoFile (also for a string that isn't an ID).
func (db *DB) FileByID(ctx context.Context, id string) (File, error) {
	if !ValidID(id) {
		return File{}, ErrNoFile
	}
	return scanFile(db.r.QueryRowContext(ctx, "SELECT "+fileColumns+" FROM files WHERE id = ?", id))
}

func scanFile(row *sql.Row) (File, error) {
	var f File
	var path []byte
	var dev, ino int64
	var state string
	var since sql.NullInt64
	err := row.Scan(&f.ID, &path, &f.Generation, &f.SHA256, &f.Stat.Size, &dev, &ino, &f.Stat.MtimeNS, &f.Stat.CtimeNS, &state, &since)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNoFile
	}
	if err != nil {
		return File{}, fmt.Errorf("sql: read a file row: %w", err)
	}
	f.Path, f.Stat.Device, f.Stat.Inode = string(path), uint64(dev), uint64(ino) //nolint:gosec // G115: the bits are kept
	f.Suspect = state == "suspect"
	if since.Valid {
		f.SuspectSince = time.UnixMilli(since.Int64)
	}
	return f, nil
}

// TrashRecord is a row of trash_records.
type TrashRecord struct {
	OpID         string
	Name         string
	OriginalPath *string // nil when unknown (imported from v0.1)
	Kind, State  string
	DeletedAt    time.Time
	Clock        time.Time // the 7-day purge clock's start
}

// TrashRecords returns every trash record, oldest delete first.
func (db *DB) TrashRecords(ctx context.Context) ([]TrashRecord, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT op_id, name, original_path, kind, state, deleted_at_ms, clock_ms
		FROM trash_records ORDER BY deleted_at_ms, op_id`)
	if err != nil {
		return nil, fmt.Errorf("sql: read the trash records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	recs := []TrashRecord{}
	for rows.Next() {
		var r TrashRecord
		var name, orig []byte
		var deleted, clock int64
		if err := rows.Scan(&r.OpID, &name, &orig, &r.Kind, &r.State, &deleted, &clock); err != nil {
			return nil, fmt.Errorf("sql: read the trash records: %w", err)
		}
		r.Name, r.DeletedAt, r.Clock = string(name), time.UnixMilli(deleted), time.UnixMilli(clock)
		if orig != nil {
			p := string(orig)
			r.OriginalPath = &p
		}
		recs = append(recs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sql: read the trash records: %w", err)
	}
	return recs, nil
}
