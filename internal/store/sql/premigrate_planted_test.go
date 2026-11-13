//go:build linux

package sql

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// openGuarded is open with a hang guard: SQLite opening a FIFO planted beside a file it
// reads would block for good. After a while the guard opens every FIFO in dir for writing,
// which wakes a blocked reader, and fails the test instead of hanging it.
func openGuarded(t *testing.T, dir string, fsys fs.FS) (*DB, error) {
	t.Helper()
	type result struct {
		db  *DB
		err error
	}
	ch := make(chan result, 1)
	go func() {
		db, err := open(context.Background(), dir, discard, fsys)
		ch <- result{db, err}
	}()
	select {
	case r := <-ch:
		return r.db, r.err
	case <-time.After(10 * time.Second):
	}
	unblockFIFOs(t, dir)
	select {
	case r := <-ch:
		if r.db != nil {
			_ = r.db.Close()
		}
	case <-time.After(10 * time.Second):
	}
	t.Fatal("the open hung on a planted file")
	return nil, nil
}

func unblockFIFOs(t *testing.T, dir string) {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Error(err)
		return
	}
	for _, e := range es {
		if e.Type()&fs.ModeNamedPipe != 0 {
			// O_RDWR never blocks on a FIFO (Linux), and counts as a writer for a blocked reader.
			if f, err := os.OpenFile(filepath.Join(dir, e.Name()), os.O_RDWR, 0); err == nil { //nolint:gosec // G304: the test's own files
				_ = f.Close()
			}
		}
	}
}

func mustMkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

// A stale temp and its -journal from a crashed snapshot (ADR-0015's first row) are removed,
// and the snapshot is taken again.
func TestStaleSnapshotTempJournalIsRemoved(t *testing.T) {
	dir := closedDB(t)
	fsys, from := laterMigrations(t, 2)
	snap := snapshotName(from, from+2)
	writeIntentFile(t, dir, intent{From: from, To: from + 2, Snapshot: snap})
	writeFile(t, filepath.Join(dir, snap+".tmp"), []byte("partial"))
	writeFile(t, filepath.Join(dir, snap+".tmp-journal"), []byte("stale"))
	db, err := openGuarded(t, dir, fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	assertMigrated(t, db, dir, from, from+2)
}

// The snapshot's rename never replaces a file already at its name (RENAME_NOREPLACE): one
// that appeared there is interference, left as it was.
func TestTakeSnapshotNeverReplaces(t *testing.T) {
	db, dir := newDB(t)
	snap := filepath.Join(dir, "pre-migrate.db")
	writeFile(t, snap, []byte("someone else's"))
	err := takeSnapshot(context.Background(), db, snap, len(embeddedFS(t)))
	if !errors.Is(err, ErrMigrateInterference) {
		t.Fatalf("got %v, want ErrMigrateInterference", err)
	}
	if b, err := os.ReadFile(snap); err != nil || string(b) != "someone else's" { //nolint:gosec // G304: the test's own file
		t.Fatalf("the file at the snapshot's name was replaced: %q, %v", b, err)
	}
}

// A snapshot replaced while it was verified is refused: the file checked must be the file
// still there.
func TestSnapshotReplacedWhileVerified(t *testing.T) {
	dir := closedDB(t)
	fsys, from := laterMigrations(t, 2)
	in := intent{From: from, To: from + 2, Snapshot: snapshotName(from, from+2)}
	snap := filepath.Join(dir, in.Snapshot)
	snapshotAt(t, embeddedFS(t), snap)
	other := filepath.Join(t.TempDir(), "other.db")
	snapshotAt(t, embeddedFS(t), other)
	writeIntentFile(t, dir, in)
	setKillPoint(t, func(p string) {
		if p == "snapshot-rechecked" {
			if err := os.Rename(other, snap); err != nil {
				t.Error(err)
			}
		}
	})
	db, err := openGuarded(t, dir, fsys)
	if !errors.Is(err, ErrMigrateInterference) {
		if db != nil {
			_ = db.Close()
		}
		t.Fatalf("got %v, want ErrMigrateInterference", err)
	}
	if v := committedVersion(t, dir); v != from {
		t.Fatalf("a refused open migrated to %d", v)
	}
}

// Snapshot (shared with backups) and checkCopy never open a copy with a file SQLite would
// open beside it: a FIFO there would block them for good.
func TestCopyWithASidecarIsRefused(t *testing.T) {
	db, _ := newDB(t)
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "copy.db")
			mustMkfifo(t, dest+suffix)
			done := make(chan error, 1)
			go func() { _, err := db.Snapshot(context.Background(), dest); done <- err }()
			if err := waitOrUnblock(t, dir, done); !errors.Is(err, ErrCopySidecar) {
				t.Fatalf("Snapshot: got %v, want ErrCopySidecar", err)
			}
			if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the refused copy is left: %v", err)
			}
			whole := filepath.Join(t.TempDir(), "copy.db")
			snapshotAt(t, embeddedFS(t), whole)
			if err := os.Rename(whole, dest); err != nil {
				t.Fatal(err)
			}
			go func() { _, err := checkCopy(context.Background(), dest); done <- err }()
			if err := waitOrUnblock(t, dir, done); !errors.Is(err, ErrCopySidecar) {
				t.Fatalf("checkCopy: got %v, want ErrCopySidecar", err)
			}
		})
	}
}

// waitOrUnblock waits for done, or fails after unblocking any FIFO in dir.
func waitOrUnblock(t *testing.T, dir string, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
	}
	unblockFIFOs(t, dir)
	select {
	case err := <-done:
		t.Fatalf("hung on a planted FIFO until it was opened (then: %v)", err)
	case <-time.After(10 * time.Second):
	}
	t.Fatal("hung on a planted FIFO")
	return nil
}

// readIntent's own guards, which the runner's later checks would otherwise mask: an intent
// this runner could never have written is interference; an I/O error is returned as one,
// without the path.
func TestReadIntent(t *testing.T) {
	for _, in := range []intent{
		{From: 0, To: 2, Snapshot: snapshotName(0, 2)},
		{From: 2, To: 2, Snapshot: snapshotName(2, 2)},
		{From: 3, To: 2, Snapshot: snapshotName(3, 2)},
	} {
		dir := t.TempDir()
		writeIntentFile(t, dir, in)
		if _, err := readIntent(dir); !errors.Is(err, ErrMigrateInterference) {
			t.Fatalf("%+v: got %v, want ErrMigrateInterference", in, err)
		}
	}
	dir := t.TempDir()
	writeIntentFile(t, dir, intent{From: 1, To: 2, Snapshot: snapshotName(1, 2)})
	if got, err := readIntent(dir); err != nil || got == nil || got.From != 1 || got.To != 2 {
		t.Fatalf("a good intent: %+v, %v", got, err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file")
	}
	if err := os.Chmod(filepath.Join(dir, MigrateIntent), 0); err != nil {
		t.Fatal(err)
	}
	_, err := readIntent(dir)
	if !errors.Is(err, fs.ErrPermission) || errors.Is(err, ErrMigrateInterference) || strings.Contains(err.Error(), dir) {
		t.Fatalf("an unreadable intent: got %v, want a permission error without the path", err)
	}
}

// markStarted's late check (a defence: checkPlanted refuses first) reports interference once.
func TestMarkStartedRefusesAFolderOnce(t *testing.T) {
	db, dir := newDB(t)
	in := intent{From: 1, To: 2, Snapshot: snapshotName(1, 2)}
	mustMkdir(t, filepath.Join(dir, markerName(1, 2)))
	err := markStarted(context.Background(), db, in)
	if !errors.Is(err, ErrMigrateInterference) || strings.Count(err.Error(), ErrMigrateInterference.Error()) != 1 {
		t.Fatalf("got %v, want ErrMigrateInterference once", err)
	}
}

// createExclusive's error names the kind of file it was creating.
func TestCreateExclusiveNamesTheFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := createExclusive(filepath.Join(dir, "missing", "x"), "migration intent"); err == nil ||
		!strings.Contains(err.Error(), "sql: create the migration intent: ") {
		t.Fatalf("got %v", err)
	}
	writeFile(t, filepath.Join(dir, "x"), nil)
	if _, err := createExclusive(filepath.Join(dir, "x"), "post_migration_started file"); !errors.Is(err, os.ErrExist) ||
		!strings.Contains(err.Error(), "sql: create the post_migration_started file: ") {
		t.Fatalf("got %v", err)
	}
}
