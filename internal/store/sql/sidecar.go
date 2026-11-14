//go:build linux

package sql

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// The intents in the state directory (migrate.intent, rollback.intent) are small JSON
// sidecar files: read without following a symlink, written by temp, fsync, rename and folder
// fsync, so each is either absent or whole.

const sidecarMax = 4096 // a larger file isn't ours

// readSidecar reads stateDir/name: nil if absent. A symlink, a file that isn't regular or
// one over sidecarMax is bad (the caller's interference error); an I/O error names the file
// by name, never by path.
func readSidecar(stateDir, name string, bad error) ([]byte, error) {
	f, err := os.OpenFile(filepath.Join(stateDir, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: the state directory's own file
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%w (%s is a symlink)", bad, name)
	}
	if err != nil {
		return nil, sidecarIOError(name, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, sidecarIOError(name, err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > sidecarMax {
		return nil, fmt.Errorf("%w (%s isn't a small regular file)", bad, name)
	}
	b, err := io.ReadAll(io.LimitReader(f, sidecarMax+1))
	if err != nil {
		return nil, sidecarIOError(name, err)
	}
	if len(b) > sidecarMax { // it grew since the Stat
		return nil, fmt.Errorf("%w (%s isn't a small regular file)", bad, name)
	}
	return b, nil
}

// sidecarIOError is an I/O error reading a sidecar, which isn't interference: it keeps the
// operation and the cause, and names the file by its fixed name instead of its path.
func sidecarIOError(name string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = &fs.PathError{Op: pe.Op, Path: name, Err: pe.Err}
	}
	return fmt.Errorf("sql: read %s: %w", name, err)
}

// writeSidecar makes stateDir/name durable with content b: written to a temp (a stale one
// from a crash is removed first), fsynced, renamed into place with RENAME_NOREPLACE (the
// caller found none under the instance lock; one that appeared is bad) and the folder
// fsynced. The kill points synced and written fire after the temp's fsync and after the
// folder's.
func writeSidecar(stateDir, name string, b []byte, bad error, synced, written string) error {
	path := filepath.Join(stateDir, name)
	tmp := path + ".tmp"
	if err := removeRegular(tmp); err != nil {
		return err
	}
	f, err := createExclusive(tmp, name)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("sql: write %s: %w", name, err)
	}
	killPoint(synced)
	if err := renameNoReplace(tmp, path); errors.Is(err, os.ErrExist) {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w (%s appeared at its name)", bad, name)
	} else if err != nil {
		return fmt.Errorf("sql: write %s: %w", name, err)
	}
	if err := syncDir(path); err != nil {
		return fmt.Errorf("sql: write %s: %w", name, err)
	}
	killPoint(written)
	return nil
}

// removeSidecar removes stateDir/name durably; killed fires between the unlink and the
// folder's fsync.
func removeSidecar(stateDir, name, killed string) error {
	path := filepath.Join(stateDir, name)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("sql: remove %s: %w", name, err)
	}
	killPoint(killed)
	return syncDir(path)
}
