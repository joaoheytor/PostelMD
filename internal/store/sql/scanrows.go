//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The scan's reads: the rows it compares the vault with, the holds, and the tombstones'
// expiry (ADR-0031: kept 30 days, removed by a background job without the vault mutex).

// Files returns every row of files, live and suspect.
func (db *DB) Files(ctx context.Context) ([]File, error) {
	return db.queryFiles(ctx, "SELECT "+fileColumns+" FROM files")
}

// FilesMatching returns the rows a read's single-path pass matches a new path with (ADR-0036):
// every row, live or suspect, with that hash or that device and inode, and every row that
// holds a path. The indexes files_by_sha256 and files_by_inode serve the first two.
func (db *DB) FilesMatching(ctx context.Context, sha256 []byte, device, inode uint64) ([]File, error) {
	return db.queryFiles(ctx, "SELECT "+fileColumns+` FROM files WHERE sha256 = ? OR (device = ? AND inode = ?)
		OR id IN (SELECT file_id FROM pending)`, sha256, int64(device), int64(inode)) //nolint:gosec // G115: the bits are kept
}

func (db *DB) queryFiles(ctx context.Context, query string, args ...any) ([]File, error) {
	rows, err := db.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sql: read the file rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []File{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sql: read the file rows: %w", err)
	}
	return out, nil
}

// Pending is a row of pending: a path held for a rule-2 rename, waiting for FileID's
// suspect to be resolved.
type Pending struct {
	Path   string
	FileID string
	SHA256 []byte
	Stat   FileStat
}

// Holds returns every held path.
func (db *DB) Holds(ctx context.Context) ([]Pending, error) {
	rows, err := db.r.QueryContext(ctx, "SELECT path, file_id, sha256, size, device, inode, mtime_ns, ctime_ns FROM pending")
	if err != nil {
		return nil, fmt.Errorf("sql: read the holds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Pending{}
	for rows.Next() {
		var p Pending
		var path []byte
		var dev, ino int64
		if err := rows.Scan(&path, &p.FileID, &p.SHA256, &p.Stat.Size, &dev, &ino, &p.Stat.MtimeNS, &p.Stat.CtimeNS); err != nil {
			return nil, fmt.Errorf("sql: read the holds: %w", err)
		}
		p.Path, p.Stat.Device, p.Stat.Inode = string(path), uint64(dev), uint64(ino) //nolint:gosec // G115: the bits are kept
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sql: read the holds: %w", err)
	}
	return out, nil
}

// Tombstone is a row of tombstones: an ended ID.
type Tombstone struct {
	ID             string
	LastPath       string
	LastGeneration int64
	At             time.Time
	Reason         string
}

// ErrNoTombstone: no tombstone has that ID (never seen, live, or expired).
var ErrNoTombstone = errors.New("sql: no tombstone with that ID")

// TombstoneByID returns the tombstone of id, or ErrNoTombstone.
func (db *DB) TombstoneByID(ctx context.Context, id string) (Tombstone, error) {
	var t Tombstone
	var path []byte
	var at int64
	err := db.r.QueryRowContext(ctx, "SELECT id, last_path, last_generation, at_ms, reason FROM tombstones WHERE id = ?", id).
		Scan(&t.ID, &path, &t.LastGeneration, &at, &t.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return Tombstone{}, ErrNoTombstone
	}
	if err != nil {
		return Tombstone{}, fmt.Errorf("sql: read a tombstone: %w", err)
	}
	t.LastPath, t.At = string(path), time.UnixMilli(at)
	return t, nil
}

// ExpireTombstones removes the tombstones recorded before cutoff and returns how many. It is
// a write of its own, never under the vault mutex (ADR-0030's background writers).
func (db *DB) ExpireTombstones(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := db.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM tombstones WHERE at_ms < ?", cutoff.UnixMilli())
		if err != nil {
			return fmt.Errorf("sql: expire tombstones: %w", err)
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}
