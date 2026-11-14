//go:build linux

package sql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// continueRollback takes a rollback from whatever step its intent's files show, to the end.
// Each step reads what is there before acting; nothing is inferred from one signal: a file
// at app.db after the original moved is the restored copy only if its bytes are the
// snapshot's, with no WAL, shared-memory or journal file beside it.
func continueRollback(ctx context.Context, stateDir string, rb rollbackIntent) (RolledBack, error) {
	done := RolledBack{From: rb.From, To: rb.To, Dir: rb.Dir, Restored: true}
	d := filepath.Join(stateDir, rb.Dir)
	snap, snapMoved := filepath.Join(stateDir, rb.Snapshot), filepath.Join(d, rb.Snapshot)
	if err := makeRollbackDir(stateDir, d); err != nil {
		return done, err
	}
	inState, err := regularFile(snap)
	if err != nil {
		return done, interference(err)
	}
	moved, err := regularFile(snapMoved)
	switch {
	case err != nil:
		return done, interference(err)
	case moved && inState:
		return done, interference(errors.New("the snapshot is in both places"))
	case !moved && !inState:
		return done, interference(errors.New("the snapshot is in neither place"))
	case !moved:
		if err := restore(ctx, stateDir, d, snap, rb.From); err != nil {
			return done, err
		}
		if err := moveInto(snap, snapMoved); err != nil {
			return done, err
		}
		killPoint("rollback-snapshot-moved")
		if err := syncBoth(snap, snapMoved); err != nil {
			return done, err
		}
	}
	in, err := readIntent(stateDir)
	switch {
	case err != nil:
		return done, interference(err)
	case in != nil && (in.From != rb.From || in.To != rb.To):
		return done, interference(fmt.Errorf("migrate.intent names %d → %d", in.From, in.To))
	case in != nil:
		if err := removeSidecar(stateDir, MigrateIntent, "rollback-retired"); err != nil {
			return done, err
		}
	}
	return done, removeSidecar(stateDir, RollbackIntent, "rollback-done")
}

// interference wraps what a step found as ErrRollbackInterference.
func interference(err error) error { return fmt.Errorf("%w: %w", ErrRollbackInterference, err) }

// makeRollbackDir creates the rollback folder (0700, durable) unless it is there. Absent, no
// file can have moved yet, so app.db must still be in the state directory.
func makeRollbackDir(stateDir, d string) error {
	fi, err := os.Lstat(d)
	switch {
	case err == nil && fi.IsDir():
		return nil
	case err == nil:
		return interference(fmt.Errorf("%s isn't a folder", filepath.Base(d)))
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if ok, err := regularFile(filepath.Join(stateDir, AppDB)); err != nil || !ok {
		return interference(errors.Join(errors.New("app.db is missing, and nothing has moved yet"), err))
	}
	if err := os.Mkdir(d, 0o700); err != nil {
		return fmt.Errorf("sql: create the rollback folder: %w", err)
	}
	if err := syncDir(d); err != nil {
		return err
	}
	killPoint("rollback-dir-made")
	return nil
}

// restore puts the snapshot's copy at app.db: app.db and the files beside it move into d
// first, then the copy is made, verified and renamed into place, never beside a WAL.
func restore(ctx context.Context, stateDir, d, snap string, from int) error {
	db := filepath.Join(stateDir, AppDB)
	orig, err := regularFile(filepath.Join(d, AppDB))
	if err != nil {
		return interference(err)
	}
	cur, err := regularFile(db)
	if err != nil {
		return interference(err)
	}
	if orig && cur { // restored before a crash, or another file
		if err := noneBeside(db); err != nil {
			return err
		}
		same, err := sameContent(db, snap)
		if err != nil || !same {
			return interference(errors.Join(errors.New("app.db is neither the moved database nor the snapshot's copy"), err))
		}
		return nil
	}
	if !orig && !cur {
		return interference(errors.New("app.db is in neither place"))
	}
	if err := moveAside(stateDir, d); err != nil {
		return err
	}
	tmp := db + ".tmp"
	if err := removeRegular(tmp); err != nil {
		return interference(err)
	}
	if err := copyFile(snap, tmp); err != nil {
		return err
	}
	killPoint("rollback-copied")
	if v, err := checkCopy(ctx, tmp); err != nil || v != from {
		return interference(errors.Join(fmt.Errorf("the snapshot's copy doesn't verify (version %d, want %d)", v, from), err))
	}
	killPoint("rollback-verified")
	if err := noneBeside(db); err != nil {
		return err
	}
	if err := renameNoReplace(tmp, db); err != nil {
		return interference(fmt.Errorf("put the snapshot's copy in place: %w", err))
	}
	killPoint("rollback-restored")
	if err := syncDir(db); err != nil {
		return err
	}
	killPoint("rollback-restored-synced")
	return nil
}

// moveAside moves app.db and each file beside it that is there into d, then makes the moves
// durable. A name already taken in d is interference: nothing is ever overwritten.
func moveAside(stateDir, d string) error {
	for _, n := range dbFiles {
		ok, err := regularFile(filepath.Join(stateDir, n))
		if err != nil {
			return interference(err)
		}
		if !ok {
			continue
		}
		if err := moveInto(filepath.Join(stateDir, n), filepath.Join(d, n)); err != nil {
			return err
		}
		killPoint("rollback-moved")
	}
	if err := syncBoth(filepath.Join(stateDir, AppDB), filepath.Join(d, AppDB)); err != nil {
		return err
	}
	killPoint("rollback-moved-synced")
	return nil
}

// noneBeside refuses a WAL, shared-memory or journal file beside app.db: SQLite would replay
// it into the restored file (ADR-0015).
func noneBeside(db string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(db + suffix); err == nil {
			return interference(fmt.Errorf("an app.db%s is beside the restored database", suffix))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// moveInto renames from to to, never replacing to.
func moveInto(from, to string) error {
	if err := renameNoReplace(from, to); errors.Is(err, os.ErrExist) {
		return interference(fmt.Errorf("%s is already in the rollback folder", filepath.Base(to)))
	} else if err != nil {
		return fmt.Errorf("sql: move %s: %w", filepath.Base(from), err)
	}
	return nil
}

// syncBoth fsyncs the folders holding a and b.
func syncBoth(a, b string) error {
	if err := syncDir(a); err != nil {
		return err
	}
	return syncDir(b)
}

// copyFile copies src (a regular file, not followed) to dst, created 0600 with O_EXCL and
// fsynced (createExclusive), byte for byte.
func copyFile(src, dst string) (err error) {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: the state directory's own snapshot
	if err != nil {
		return interference(fmt.Errorf("open the snapshot: %w", err))
	}
	defer func() { _ = in.Close() }()
	out, err := createExclusive(dst, "restored database")
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("sql: copy the snapshot: %w", err)
	}
	return out.Sync()
}

// sameContent reports whether the regular files a and b hold the same bytes.
func sameContent(a, b string) (bool, error) {
	fa, err := os.OpenFile(a, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: the state directory's own files
	if err != nil {
		return false, err
	}
	defer func() { _ = fa.Close() }()
	fb, err := os.OpenFile(b, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: the state directory's own files
	if err != nil {
		return false, err
	}
	defer func() { _ = fb.Close() }()
	ba, bb := make([]byte, 1<<16), make([]byte, 1<<16)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false, nil
		}
		aEnd := errors.Is(ea, io.EOF) || errors.Is(ea, io.ErrUnexpectedEOF)
		bEnd := errors.Is(eb, io.EOF) || errors.Is(eb, io.ErrUnexpectedEOF)
		switch {
		case ea != nil && !aEnd:
			return false, ea
		case eb != nil && !bEnd:
			return false, eb
		case aEnd || bEnd:
			return aEnd == bEnd, nil
		}
	}
}

// retire undoes a migration that stopped before its snapshot was in place: app.db is still
// at from (checkEligible read it), so it removes the snapshot's temp and its journal, then
// migrate.intent. Each step is idempotent and migrate.intent is the durable record, so a
// crash leaves a state the next run finishes (or, after the last step, has nothing to do).
func retire(stateDir string, from, to int) (RolledBack, error) {
	done := RolledBack{From: from, To: to}
	snap := filepath.Join(stateDir, snapshotName(from, to))
	for _, p := range []string{snap + ".tmp", snap + ".tmp-journal"} {
		if err := removeRegular(p); err != nil {
			return done, err
		}
	}
	if err := syncDir(snap); err != nil {
		return done, err
	}
	killPoint("rollback-cleaned")
	return done, removeSidecar(stateDir, MigrateIntent, "rollback-retired")
}
