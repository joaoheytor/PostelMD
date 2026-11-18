//go:build linux

package sql

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// maxCopies bounds the names KeepIntentCopy tries for one side: each recovery of an intent
// that keeps copying a side that keeps changing adds one.
const maxCopies = 16

// KeepIntentCopy makes what open yields durable as name in the state directory's
// recovery/<opID>/ (ADR-0033: interference keeps both sides; a dropped save or create keeps
// a complete temp), owner only, through a copy beside the folder renamed into it. A file
// already there with the same bytes is the copy a crash left (nil). One with other bytes
// keeps them: the copy goes to name-1, name-2 and so on, so no copy is ever overwritten.
func KeepIntentCopy(stateDir, opID, name string, open func() (io.ReadCloser, error)) error {
	if !plainName(opID) || strings.HasPrefix(opID, "rebuild-") || !plainName(name) {
		return fmt.Errorf("sql: not an intent's recovery copy: %q, %q", opID, name)
	}
	rec := filepath.Join(stateDir, "recovery")
	dir := filepath.Join(rec, opID)
	for _, p := range []string{rec, dir} {
		if err := mkdirDurable(p); err != nil {
			return err
		}
	}
	for i := 0; i < maxCopies; i++ {
		dst := filepath.Join(dir, name)
		if i > 0 {
			dst += "-" + strconv.Itoa(i)
		}
		ok, err := regularFile(dst)
		if err != nil {
			return fmt.Errorf("sql: check a recovery copy: %w", err)
		}
		if !ok {
			if _, err := os.Lstat(dst); err == nil {
				continue // not a regular file: never written over
			}
			return copyInto(rec, dst, open)
		}
		src, err := open()
		if err != nil {
			return err
		}
		same, err := sameBytes(dst, src)
		_ = src.Close()
		if err != nil {
			return fmt.Errorf("sql: read a recovery copy: %w", err)
		}
		if same {
			return nil
		}
	}
	return errors.New("sql: too many recovery copies of one side of an intent")
}

// copyInto streams open's bytes into a copy in rec, synced, then renames it to dst without
// replacing anything, and syncs both folders.
func copyInto(rec, dst string, open func() (io.ReadCloser, error)) error {
	tmp := filepath.Join(rec, "."+filepath.Base(filepath.Dir(dst))+"."+filepath.Base(dst)+".copying")
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sql: remove the leftover copy: %w", err)
	}
	src, err := open()
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	f, err := createExclusive(tmp, "recovery copy")
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
		err = renameNoReplace(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("sql: keep a recovery copy: %w", err)
	}
	return syncBoth(tmp, dst)
}

func plainName(s string) bool {
	return s != "" && s != "." && s != ".." && len(s) <= 255 && !strings.ContainsAny(s, "/\x00")
}
