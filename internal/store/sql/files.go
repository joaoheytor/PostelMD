//go:build linux

package sql

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
)

// createExclusive creates path empty, 0600, with O_CREAT|O_EXCL, and syncs it and its
// folder, before SQLite opens or writes it: SQLite gives the -wal and -shm files the mode of
// the database file, so all three stay 0600 (ADR-0030). It returns the open file, which the
// caller closes. An existing path is os.ErrExist. Errors name what (the database file, the
// migration intent, …), never the path.
func createExclusive(path, what string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: the state directory's own files
	if err != nil {
		return nil, fmt.Errorf("sql: create the %s: %w", what, err)
	}
	// The umask can only remove bits from 0600; Fchmod makes the mode exact all the same.
	err = f.Chmod(0o600)
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = syncDir(path)
	}
	if err != nil {
		_ = f.Close()
		_ = os.Remove(path) // a partial file is removed on error (ADR-0030)
		return nil, fmt.Errorf("sql: create the %s: %w", what, err)
	}
	return f, nil
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

// checkRegular refuses a database path that is a symlink or not a regular file (SQLite
// would follow a symlink), and returns what it found, so the caller can check that the file
// SQLite opened is the same one.
func checkRegular(path string) (os.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("sql: the database file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("sql: the database file is not a regular file (%s); move it aside", fi.Mode().Type())
	}
	return fi, nil
}

// fixSidecars checks an existing -wal and -shm beside path before SQLite opens them: each
// must be a regular file (a symlink is refused, never followed), and one with group or world
// bits, left by an older copy or a hand edit, is set to 0600 so the WAL's content isn't
// readable by others.
func fixSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		f, err := os.OpenFile(path+suffix, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: the state directory's own files
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("sql: the database's %s file can't be opened as a regular file: %w", suffix, err)
		}
		err = checkSidecar(f, suffix)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func checkSidecar(f *os.File, suffix string) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("sql: the database's %s file is not a regular file (%s); move it aside", suffix, fi.Mode().Type())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return f.Chmod(0o600)
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
