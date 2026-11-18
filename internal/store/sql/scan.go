//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The startup scan's writes (ADR-0031, ADR-0036). A pass is applied in one transaction, as
// units: each unit is a savepoint, and a unit whose rows changed since the scan read them
// (the precondition each change carries) is rolled back alone and left to the next pass.

// errStale: a change's row isn't what the scan read. Its unit is rolled back; the rest of the
// pass commits.
var errStale = errors.New("sql: the row changed since the scan read it")

// Change is one write of a scan unit: UpdateFile, Suspect, TombstoneFile, InsertFile, Hold
// or Release.
type Change interface {
	apply(ctx context.Context, tx *sql.Tx, now time.Time) error
}

// Row names the row a change expects: its ID, path and generation as the scan read them.
type Row struct {
	ID         string
	Path       string
	Generation int64
}

// UpdateFile sets a row's path, hash and stat, and makes it live: a changed file, a hash the
// first assignment couldn't take, a suspect that is back, or an ID that moves to a new path.
// Bump adds one to the generation.
type UpdateFile struct {
	Row
	NewPath string
	SHA256  []byte
	Stat    FileStat
	Bump    bool
}

// Suspect marks a live row suspect from the transaction's time: a scan missed its path. On a
// suspect row it only restarts a clock that is in the future (the system clock stepped back,
// ADR-0036); a suspect already counting is stale.
type Suspect struct{ Row }

// TombstoneFile ends a row's ID: the row moves to tombstones with its last path and
// generation, and its pending hold, if any, goes with it (ON DELETE CASCADE).
type TombstoneFile struct {
	Row
	Reason string // "missing" or "ambiguous-rename" for the scan
}

// InsertFile gives a path that has no row a new ID at generation 1.
type InsertFile struct {
	Path   string
	SHA256 []byte
	Stat   FileStat
}

// Hold records a rule-2 rename: Path waits, without an ID, for the suspect FileID.
type Hold struct {
	Path   string
	FileID string
	SHA256 []byte
	Stat   FileStat
}

// Release drops the hold on Path, if there is one.
type Release struct{ Path string }

// ApplyScan applies units in one write transaction, each in its own savepoint, and reports
// which were applied. A unit whose rows changed since the scan read them is skipped; any
// other error rolls back the whole transaction. now is the pass's time: new IDs, the
// suspects' clock and the tombstones get it. The caller holds the vault mutex (ADR-0030's
// lock order) and has re-checked each unit's paths on disk: nothing here touches the vault.
func (db *DB) ApplyScan(ctx context.Context, now time.Time, units [][]Change) ([]bool, error) {
	applied := make([]bool, len(units))
	err := db.writeTx(ctx, func(tx *sql.Tx) error {
		for i, u := range units {
			ok, err := applyUnit(ctx, tx, now, u)
			if err != nil {
				return err
			}
			applied[i] = ok
		}
		killPoint("scan-before-commit")
		return nil
	})
	if err != nil {
		return nil, err
	}
	killPoint("scan-after-commit")
	return applied, nil
}

func applyUnit(ctx context.Context, tx *sql.Tx, now time.Time, u []Change) (bool, error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT unit"); err != nil {
		return false, err
	}
	for _, c := range u {
		err := c.apply(ctx, tx, now)
		if errors.Is(err, errStale) {
			if _, err := tx.ExecContext(ctx, "ROLLBACK TO unit"); err != nil {
				return false, err
			}
			_, err = tx.ExecContext(ctx, "RELEASE unit")
			return false, err
		}
		if err != nil {
			return false, err
		}
	}
	_, err := tx.ExecContext(ctx, "RELEASE unit")
	return err == nil, err
}

// one runs a statement that must change exactly one row; none is errStale.
func one(ctx context.Context, tx *sql.Tx, what, query string, args ...any) error {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("sql: %s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sql: %s: %w", what, err)
	}
	if n != 1 {
		return errStale
	}
	return nil
}

func (c UpdateFile) apply(ctx context.Context, tx *sql.Tx, _ time.Time) error {
	if c.NewPath != c.Path { // a moved ID: the new path must still be free
		if taken, err := pathTaken(ctx, tx, c.NewPath); err != nil || taken {
			return errors.Join(err, staleIf(taken))
		}
	}
	bump := 0
	if c.Bump {
		bump = 1
	}
	return one(ctx, tx, "update a file row", `UPDATE files SET path = ?, generation = generation + ?, sha256 = ?,
		size = ?, device = ?, inode = ?, mtime_ns = ?, ctime_ns = ?, state = 'live', suspect_since_ms = NULL,
		displaced_from = NULL WHERE id = ? AND path = ? AND generation = ?`,
		append([]any{[]byte(c.NewPath), bump, hashArg(c.SHA256)}, append(statArgs(c.Stat), c.ID, []byte(c.Path), c.Generation)...)...)
}

func (c Suspect) apply(ctx context.Context, tx *sql.Tx, now time.Time) error {
	return one(ctx, tx, "mark a file suspect", `UPDATE files SET state = 'suspect', suspect_since_ms = ?1
		WHERE id = ?2 AND path = ?3 AND generation = ?4 AND (state = 'live' OR suspect_since_ms > ?1)`,
		now.UnixMilli(), c.ID, []byte(c.Path), c.Generation)
}

func (c TombstoneFile) apply(ctx context.Context, tx *sql.Tx, now time.Time) error {
	// A vacated row's tombstone names the path it had, not its placeholder (ADR-0033).
	if err := one(ctx, tx, "record a tombstone", `INSERT INTO tombstones (id, last_path, last_generation, at_ms, reason)
		SELECT id, coalesce(displaced_from, path), generation, ?, ? FROM files WHERE id = ? AND path = ? AND generation = ?`,
		now.UnixMilli(), c.Reason, c.ID, []byte(c.Path), c.Generation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM files WHERE id = ?", c.ID); err != nil {
		return fmt.Errorf("sql: end a file ID: %w", err)
	}
	return nil
}

func (c InsertFile) apply(ctx context.Context, tx *sql.Tx, now time.Time) error {
	if taken, err := pathTaken(ctx, tx, c.Path); err != nil || taken {
		return errors.Join(err, staleIf(taken))
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO files (id, path, generation, sha256, size, device, inode, mtime_ns,
		ctime_ns, state) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, 'live')`,
		append([]any{NewID(now), []byte(c.Path), hashArg(c.SHA256)}, statArgs(c.Stat)...)...)
	if err != nil {
		return fmt.Errorf("sql: record a new file: %w", err)
	}
	return nil
}

func (c Hold) apply(ctx context.Context, tx *sql.Tx, _ time.Time) error {
	if taken, err := pathTaken(ctx, tx, c.Path); err != nil || taken {
		return errors.Join(err, staleIf(taken))
	}
	// The row must be a suspect without a hold (the unit marks it suspect first if needed).
	var free int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM files WHERE id = ?1 AND state = 'suspect'
		AND NOT EXISTS (SELECT 1 FROM pending WHERE file_id = ?1)`, c.FileID).Scan(&free); err != nil {
		return fmt.Errorf("sql: read the holds: %w", err)
	}
	if free != 1 {
		return errStale
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO pending (path, file_id, sha256, size, device, inode, mtime_ns, ctime_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, append([]any{[]byte(c.Path), c.FileID, c.SHA256}, statArgs(c.Stat)...)...)
	if err != nil {
		return fmt.Errorf("sql: record a held path: %w", err)
	}
	return nil
}

func (c Release) apply(ctx context.Context, tx *sql.Tx, _ time.Time) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM pending WHERE path = ?", []byte(c.Path)); err != nil {
		return fmt.Errorf("sql: release a held path: %w", err)
	}
	return nil
}

// pathTaken reports whether a row or a hold already has path.
func pathTaken(ctx context.Context, tx *sql.Tx, path string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM files WHERE path = ?1) +
		(SELECT count(*) FROM pending WHERE path = ?1)`, []byte(path)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("sql: look a path up: %w", err)
	}
	return n != 0, nil
}

func staleIf(b bool) error {
	if b {
		return errStale
	}
	return nil
}

// hashArg is NULL for a file not hashed yet, never an empty BLOB.
func hashArg(h []byte) any {
	if h == nil {
		return nil
	}
	return h
}

func statArgs(s FileStat) []any {
	return []any{s.Size, int64(s.Device), int64(s.Inode), s.MtimeNS, s.CtimeNS} //nolint:gosec // G115: the bits are kept
}
