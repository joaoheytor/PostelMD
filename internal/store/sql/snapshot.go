//go:build linux

package sql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	sqlite "modernc.org/sqlite"
)

// copyQuery opens a copy to check it: read-only, and without journal_mode(WAL), which a
// mode=ro connection can't set on VACUUM INTO's output (a rollback-journal file).
const copyQuery = "_defensive=1&_pragma=cell_size_check(ON)&_pragma=trusted_schema(OFF)&mode=ro"

var (
	// ErrBadCopy: a copy failed its integrity_check; it was removed.
	ErrBadCopy = errors.New("sql: the database copy failed its integrity check")
	// ErrCopyReplaced: the copy's path no longer names the file Snapshot created.
	ErrCopyReplaced = errors.New("sql: the database copy was replaced while it was written")
	// ErrCopySidecar: a -journal, -wal or -shm file is beside the copy; it was left as it was.
	ErrCopySidecar = errors.New("sql: a file is beside the database copy (move it aside)")
)

// Snapshot copies the database's last committed state into dest with VACUUM INTO, on a
// snapshot connection opened for it and closed after (mode=ro without query_only: read-only
// for app.db, ATTACH denied; ADR-0030). dest is created empty, 0600, with O_EXCL first, so
// an existing dest is refused; after the copy, dest must still be that file, which is then
// fsynced through the descriptor kept from its creation (SQLite doesn't sync it) and checked
// with integrity_check. It returns the copy's user_version.
//
// On an error Snapshot removes the copy, if it is still the file it created. After a crash
// a partial copy can stay behind: the caller's intent (migrate.intent, the backup's) owns
// removing it, and the caller verifies a copy (as checkCopy does) before trusting it.
func (db *DB) Snapshot(ctx context.Context, dest string) (version int, err error) {
	if err := checkAbs(dest); err != nil {
		return 0, err
	}
	dest = filepath.Clean(dest)
	f, err := createExclusive(dest, "database copy")
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil && sameFile(f, dest) {
			_ = os.Remove(dest)
		}
		_ = f.Close()
	}()
	if err := noCopySidecars(dest); err != nil {
		return 0, err
	}
	if err := vacuumInto(ctx, db.path, dest); err != nil {
		return 0, fmt.Errorf("sql: copy the database: %w", err)
	}
	killPoint("copied")
	if !sameFile(f, dest) {
		return 0, ErrCopyReplaced
	}
	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("sql: sync the copy: %w", err)
	}
	v, err := checkCopy(ctx, dest)
	if err == nil && !sameFile(f, dest) {
		err = ErrCopyReplaced
	}
	return v, err
}

// vacuumInto runs VACUUM INTO dest on a snapshot connection to path. VACUUM INTO attaches
// its output, so this one connection's authorizer allows an ATTACH of exactly dest, and
// nothing else, for the copy; no other statement runs on it.
func vacuumInto(ctx context.Context, path, dest string) error {
	snap, err := openPool(path, snapshotQuery, 1)
	if err != nil {
		return err
	}
	defer func() { _ = snap.Close() }()
	conn, err := snap.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	err = conn.Raw(func(dc any) error {
		r, ok := dc.(sqlite.AuthorizerRegisterer)
		if !ok {
			return errors.New("sql: the driver has no authorizer")
		}
		return r.RegisterAuthorizer(func(a sqlite.AuthorizerActionCode, arg1, arg2, dbName, trig string) sqlite.AuthorizerReturnCode {
			if a == sqlite.AuthAttach && arg1 == dest {
				return sqlite.AuthorizerOK
			}
			return denyAttach(a, arg1, arg2, dbName, trig)
		})
	})
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "VACUUM INTO ?", dest)
	return err
}

// sameFile reports whether path (not followed) is still the file f.
func sameFile(f *os.File, path string) bool {
	a, err := f.Stat()
	if err != nil {
		return false
	}
	b, err := os.Lstat(path)
	return err == nil && os.SameFile(a, b)
}

// noCopySidecars refuses a -journal, -wal or -shm beside a copy: none is ever left by
// VACUUM INTO or a mode=ro check, and SQLite opening one that is a FIFO blocks for good.
func noCopySidecars(dest string) error {
	return noSidecars(dest, "-journal", "-wal", "-shm")
}

// noSidecars refuses a file of any type at dest+suffix for each suffix.
func noSidecars(dest string, suffixes ...string) error {
	for _, suffix := range suffixes {
		_, err := os.Lstat(dest + suffix)
		if err == nil {
			return fmt.Errorf("%w (%s)", ErrCopySidecar, suffix)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("sql: check beside the database copy: %w", err)
		}
	}
	return nil
}

// checkCopy runs integrity_check on the copy at dest, read-only, and reads its user_version.
// It opens nothing beside a file it hasn't checked: dest must be a regular file, with no
// sidecar SQLite would open with it.
func checkCopy(ctx context.Context, dest string) (int, error) {
	if fi, err := os.Lstat(dest); err != nil || !fi.Mode().IsRegular() {
		return 0, errors.Join(ErrBadCopy, err)
	}
	if err := noCopySidecars(dest); err != nil {
		return 0, err
	}
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
