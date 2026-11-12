//go:build linux

package sql

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// createExclusive creates path empty, 0600, with O_CREAT|O_EXCL, and syncs it and its
// folder, before SQLite opens or writes it: SQLite gives the -wal and -shm files the mode of
// the database file, so all three stay 0600 (ADR-0030). An existing path is os.ErrExist.
func createExclusive(path string) error {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return &os.PathError{Op: "create", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path) //nolint:gosec // G115: an fd fits in uintptr
	// The umask can only remove bits from 0600; Fchmod makes the mode exact all the same.
	err = f.Chmod(0o600)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = syncDir(path)
	}
	if err != nil {
		_ = os.Remove(path) // a partial file is removed on error (ADR-0030)
		return fmt.Errorf("sql: create the database file: %w", err)
	}
	return nil
}

// syncDir fsyncs the folder holding path, so a created file's entry is durable.
func syncDir(path string) error {
	d, err := os.Open(filepath.Dir(path)) //nolint:gosec // G304: the state directory
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// checkRegular refuses a database path that is a symlink or not a regular file: SQLite
// would follow a symlink.
func checkRegular(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("sql: the database file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("sql: the database file is not a regular file (%s); move it aside", fi.Mode().Type())
	}
	return nil
}

// warnLooseModes logs a warning when the state directory or the database has group or world
// permission bits, and goes on: Helm's fsGroup makes them 0660 (ADR-0030). The message names
// which one, never its path.
func warnLooseModes(log *slog.Logger, stateDir, path string) {
	for _, c := range []struct{ path, what string }{{stateDir, "the state directory"}, {path, "the database file"}} {
		fi, err := os.Stat(c.path)
		if err == nil && fi.Mode().Perm()&0o077 != 0 {
			log.Warn("other users have permissions on "+c.what+"; set it to owner-only (0700 for the folder, 0600 for the file)",
				"mode", fmt.Sprintf("%04o", fi.Mode().Perm()))
		}
	}
}
