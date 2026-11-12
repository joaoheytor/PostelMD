//go:build linux

package sql

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// copyQuery opens a copy to check it: read-only, and without journal_mode(WAL), which a
// mode=ro connection can't set on VACUUM INTO's output (a rollback-journal file).
const copyQuery = "_defensive=1&_pragma=cell_size_check(ON)&_pragma=trusted_schema(OFF)&mode=ro"

// ErrBadCopy: a copy failed its integrity_check; it was removed.
var ErrBadCopy = errors.New("sql: the database copy failed its integrity check")

// Snapshot copies the database's last committed state into dest with VACUUM INTO, on a
// snapshot connection opened for it and closed after (mode=ro without query_only, ADR-0030).
// dest is created empty, 0600, with O_EXCL first, so an existing dest is refused; the copy is
// fsynced (SQLite doesn't) and checked with integrity_check. It returns the copy's
// user_version. A partial copy is removed on error. Pre-migration snapshots and backups
// (ADR-0015, ADR-0017) use it.
func (db *DB) Snapshot(ctx context.Context, dest string) (version int, err error) {
	if err := createExclusive(dest); err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(dest)
		}
	}()
	snap, err := openPool(db.path, snapshotQuery, 1)
	if err != nil {
		return 0, err
	}
	_, err = snap.ExecContext(ctx, "VACUUM INTO ?", dest)
	if cerr := snap.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, fmt.Errorf("sql: copy the database: %w", err)
	}
	if err := syncFile(dest); err != nil {
		return 0, fmt.Errorf("sql: sync the copy: %w", err)
	}
	return checkCopy(ctx, dest)
}

// checkCopy runs integrity_check on the copy at dest, read-only, and reads its user_version.
func checkCopy(ctx context.Context, dest string) (int, error) {
	c, err := openPool(dest, copyQuery, 1)
	if err != nil {
		return 0, err
	}
	defer func() { _ = c.Close() }()
	var res string
	if err := c.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&res); err != nil {
		return 0, errors.Join(ErrBadCopy, err)
	}
	if res != "ok" {
		return 0, ErrBadCopy
	}
	var v int
	if err := c.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("sql: read the copy's user_version: %w", err)
	}
	return v, nil
}

// syncFile fsyncs path's data.
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // G304: a copy this package created
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
