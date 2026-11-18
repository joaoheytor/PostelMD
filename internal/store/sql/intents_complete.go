//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The transactions that record an operation's result and end its intent (ADR-0033): each
// runs once the filesystem steps are durable, at the end of the operation or from recovery.
// The outbox rows ADR-0003 adds to them come with the outbox slice.

// CompleteSave records an applied save: the file's generation + 1, its new hash and stat, and
// the intent ended, in one transaction. The row must still be the intent's base.
func (db *DB) CompleteSave(ctx context.Context, opID string, stat FileStat) error {
	return db.intentTx(ctx, "save-complete", func(tx *sql.Tx) error {
		var p SaveIntent
		if err := readOpen(ctx, tx, opID, &p); err != nil {
			return err
		}
		err := one(ctx, tx, "record a save", `UPDATE files SET generation = generation + 1, sha256 = ?, size = ?,
			device = ?, inode = ?, mtime_ns = ?, ctime_ns = ?, state = 'live', suspect_since_ms = NULL
			WHERE id = ? AND path = ? AND generation = ?`,
			append(append([]any{p.NewSHA256}, statArgs(stat)...), p.FileID, []byte(p.Path), p.BaseGeneration)...)
		if err != nil {
			return staleAs(err, ErrIntentStale)
		}
		return deleteIntent(ctx, tx, opID)
	})
}

// CompleteCreate records an applied create: a new ID at generation 1 for the destination,
// and the intent ended, in one transaction. A suspect row on the destination (a file missing
// since the scan) is tombstoned first, since files.path is unique (ADR-0031); a live one is
// ErrIntentStale. It returns the new ID.
func (db *DB) CompleteCreate(ctx context.Context, opID string, stat FileStat, now time.Time) (string, error) {
	id := NewID(now)
	err := db.intentTx(ctx, "create-complete", func(tx *sql.Tx) error {
		var p CreateIntent
		if err := readOpen(ctx, tx, opID, &p); err != nil {
			return err
		}
		if err := freePath(ctx, tx, string(p.Path), nil, now); err != nil {
			return err
		}
		return errors.Join(insertRow(ctx, tx, id, string(p.Path), p.NewSHA256, stat), deleteIntent(ctx, tx, opID))
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// CompleteMove records an applied move: every row the intent names gets its path under the
// destination and a generation bump, IDs kept, and the intent ends, in one transaction.
// stats holds the moved files' stats by ID (a rename changes the renamed item's ctime); a
// file missing from it keeps its stat, and the scan finds it. Holds on paths under the
// source are released (the next scan sees those paths again), and suspect rows on the new
// paths are tombstoned as for a create.
func (db *DB) CompleteMove(ctx context.Context, opID string, stats map[string]FileStat, now time.Time) error {
	return db.intentTx(ctx, "move-complete", func(tx *sql.Tx) error {
		var p MoveIntent
		if err := readOpen(ctx, tx, opID, &p); err != nil {
			return err
		}
		moving := map[string]bool{}
		for _, f := range p.Files {
			moving[f.ID] = true
		}
		for _, f := range p.Files {
			if err := freePath(ctx, tx, movedPath(string(f.Path), string(p.From), string(p.To)), moving, now); err != nil {
				return err
			}
		}
		for _, f := range p.Files {
			if err := moveRow(ctx, tx, f, movedPath(string(f.Path), string(p.From), string(p.To)), stats); err != nil {
				return err
			}
		}
		if err := releaseUnder(ctx, tx, string(p.From)); err != nil {
			return err
		}
		return deleteIntent(ctx, tx, opID)
	})
}

func moveRow(ctx context.Context, tx *sql.Tx, f Affected, to string, stats map[string]FileStat) error {
	q, args := "UPDATE files SET path = ?, generation = generation + 1", []any{[]byte(to)}
	if st, ok := stats[f.ID]; ok {
		q += ", size = ?, device = ?, inode = ?, mtime_ns = ?, ctime_ns = ?"
		args = append(args, statArgs(st)...)
	}
	err := one(ctx, tx, "move a file row", q+" WHERE id = ? AND path = ? AND generation = ?",
		append(args, f.ID, []byte(f.Path), f.Generation)...)
	return staleAs(err, ErrIntentStale)
}

// movedPath is p, which is from or under it, under to instead.
func movedPath(p, from, to string) string { return to + strings.TrimPrefix(p, from) }

// readOpen reads the open intent opID of p's kind into p.
func readOpen(ctx context.Context, tx *sql.Tx, opID string, p Payload) error {
	in, err := openIntent(ctx, tx, opID, p.kind())
	if err != nil {
		return err
	}
	return in.Decode(p)
}

// freePath makes path free for a row an operation puts there. The operation's no-clobber
// rename proved the path absent on disk, so a row on it (usually a suspect; a live one whose
// file went since the last scan), unless its ID is in keep (it moves itself), is tombstoned
// as missing, and a hold on it, whose file went too, is released (ADR-0031: files.path is
// unique). Refusing instead would leave recovery unable to finish the intent.
func freePath(ctx context.Context, tx *sql.Tx, path string, keep map[string]bool, now time.Time) error {
	f, err := scanFile(tx.QueryRowContext(ctx, "SELECT "+fileColumns+" FROM files WHERE path = ?", []byte(path)))
	switch {
	case errors.Is(err, ErrNoFile), err == nil && keep[f.ID]:
	case err != nil:
		return err
	default:
		err := TombstoneFile{Row: Row{ID: f.ID, Path: f.Path, Generation: f.Generation}, Reason: "missing"}.apply(ctx, tx, now)
		if err != nil {
			return staleAs(err, ErrIntentStale)
		}
	}
	return Release{Path: path}.apply(ctx, tx, now)
}

// insertRow gives path the new ID id at generation 1.
func insertRow(ctx context.Context, tx *sql.Tx, id, path string, sha256 []byte, stat FileStat) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO files (id, path, generation, sha256, size, device, inode, mtime_ns,
		ctime_ns, state) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, 'live')`, append([]any{id, []byte(path), hashArg(sha256)}, statArgs(stat)...)...)
	if err != nil {
		return fmt.Errorf("sql: record a new file: %w", err)
	}
	return nil
}

// releaseUnder drops the holds on root and on every path under it.
func releaseUnder(ctx context.Context, tx *sql.Tx, root string) error {
	lo, hi := subtree(root)
	_, err := tx.ExecContext(ctx, "DELETE FROM pending WHERE path = ? OR (path >= ? AND path < ?)", []byte(root), lo, hi)
	if err != nil {
		return fmt.Errorf("sql: release held paths: %w", err)
	}
	return nil
}

// subtree is the BLOB range of the paths under root: from root+"/" up to, not including,
// root+"0" ('0' is the byte after '/'), so a sibling such as root+"-x" is outside it.
func subtree(root string) (lo, hi []byte) { return []byte(root + "/"), []byte(root + "0") }

// FilesUnder returns the rows at root and under it, by path: what a move or delete of root
// records in its intent.
func (db *DB) FilesUnder(ctx context.Context, root string) ([]File, error) {
	lo, hi := subtree(root)
	return db.queryFiles(ctx, "SELECT "+fileColumns+" FROM files WHERE path = ? OR (path >= ? AND path < ?) ORDER BY path",
		[]byte(root), lo, hi)
}
