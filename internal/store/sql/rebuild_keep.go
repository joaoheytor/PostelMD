//go:build linux

package sql

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// KeepRecovered is db rebuild's step 3 for one temp of the vault: it streams src into name
// in r's recovery folder, durably, through a copy beside the folder renamed into it. A file
// already there with the same bytes is the copy a crash left (nil); with other bytes it is
// interference. A copy a crash left beside the folder is removed first.
func KeepRecovered(stateDir string, r Rebuild, name string, src io.Reader) error {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return fmt.Errorf("sql: not a temp's name: %q", name)
	}
	rec := filepath.Join(stateDir, "recovery")
	dir := filepath.Join(stateDir, r.RecoveryDir())
	for _, p := range []string{rec, dir} {
		if err := mkdirDurable(p); err != nil {
			return err
		}
	}
	dst := filepath.Join(dir, name)
	if ok, err := regularFile(dst); err != nil {
		return rebuildInterference(err)
	} else if ok {
		same, err := sameBytes(dst, src)
		if err != nil {
			return fmt.Errorf("sql: read a kept temp: %w", err)
		}
		if !same {
			return rebuildInterference(errors.New("another file with a temp's name is in the recovery folder"))
		}
		return nil
	}
	tmp := filepath.Join(rec, "."+filepath.Base(dir)+".copying")
	if ok, err := regularFile(tmp); err != nil {
		return rebuildInterference(err)
	} else if ok {
		if err := os.Remove(tmp); err != nil {
			return fmt.Errorf("sql: remove the leftover copy: %w", err)
		}
	}
	f, err := createExclusive(tmp, "kept temp")
	if err != nil {
		return err
	}
	_, err = io.Copy(f, src)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		killPoint("rebuild-temp-copied")
		err = renameNoReplace(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("sql: keep a temp: %w", err)
	}
	return syncBoth(tmp, dst)
}

// sameBytes reports whether the file at path holds exactly what src yields, reading both in
// chunks.
func sameBytes(path string, src io.Reader) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: the state directory's own recovery file
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	a, b := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		n, errA := io.ReadFull(f, a)
		m, errB := io.ReadFull(src, b)
		if errA != nil && !errors.Is(errA, io.EOF) && !errors.Is(errA, io.ErrUnexpectedEOF) {
			return false, errA
		}
		if errB != nil && !errors.Is(errB, io.EOF) && !errors.Is(errB, io.ErrUnexpectedEOF) {
			return false, errB
		}
		if !bytes.Equal(a[:n], b[:m]) {
			return false, nil
		}
		if errA != nil || errB != nil {
			return errA != nil && errB != nil, nil
		}
	}
}
