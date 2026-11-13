//go:build linux

package sql

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertLate fails unless err is a refusal found after the migration's first side effect:
// still ErrMigrateInterference for callers, said once, and never "nothing was changed".
func assertLate(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrMigrateLateInterference) || !errors.Is(err, ErrMigrateInterference) {
		t.Fatalf("got %v, want ErrMigrateLateInterference", err)
	}
	if msg := err.Error(); strings.Contains(msg, "nothing was changed") ||
		strings.Count(msg, ErrMigrateLateInterference.Error()) != 1 || !strings.Contains(msg, "intent stays") {
		t.Fatalf("a late refusal says %q", msg)
	}
}

// The snapshot's rename never replaces a file already at its name (RENAME_NOREPLACE): one
// that appeared there is late interference, left as it was.
func TestTakeSnapshotNeverReplaces(t *testing.T) {
	db, dir := newDB(t)
	snap := filepath.Join(dir, "pre-migrate.db")
	writeFile(t, snap, []byte("someone else's"))
	assertLate(t, takeSnapshot(context.Background(), db, snap, len(embeddedFS(t))))
	if b, err := os.ReadFile(snap); err != nil || string(b) != "someone else's" { //nolint:gosec // G304: the test's own file
		t.Fatalf("the file at the snapshot's name was replaced: %q, %v", b, err)
	}
}

// A copy at another version than from is late interference, and the copy is removed.
func TestTakeSnapshotAtAnotherVersion(t *testing.T) {
	db, dir := newDB(t)
	snap := filepath.Join(dir, "pre-migrate.db")
	assertLate(t, takeSnapshot(context.Background(), db, snap, len(embeddedFS(t))+1))
	for _, p := range []string{snap, snap + ".tmp"} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s is left: %v", filepath.Base(p), err)
		}
	}
}

// A sidecar beside the temp that appears after checkPlanted is late interference too (the
// copy's own ErrCopySidecar, wrapped), and is left as it was.
func TestTakeSnapshotSidecarIsLate(t *testing.T) {
	db, dir := newDB(t)
	snap := filepath.Join(dir, "pre-migrate.db")
	writeFile(t, snap+".tmp-wal", []byte("planted"))
	err := takeSnapshot(context.Background(), db, snap, len(embeddedFS(t)))
	assertLate(t, err)
	if !errors.Is(err, ErrCopySidecar) {
		t.Fatalf("got %v, want ErrCopySidecar too", err)
	}
	if b, err := os.ReadFile(snap + ".tmp-wal"); err != nil || string(b) != "planted" { //nolint:gosec // G304: the test's own file
		t.Fatalf("the planted -wal: %q, %v", b, err)
	}
}

// takeSnapshot checks every temp path before it removes any: a FIFO at the temp's -journal
// is refused with the stale temp still there.
func TestTakeSnapshotChecksTempsFirst(t *testing.T) {
	db, dir := newDB(t)
	snap := filepath.Join(dir, "pre-migrate.db")
	writeFile(t, snap+".tmp", []byte("partial"))
	mustMkfifo(t, snap+".tmp-journal")
	if err := takeSnapshot(context.Background(), db, snap, len(embeddedFS(t))); !errors.Is(err, ErrMigrateInterference) {
		t.Fatalf("got %v, want ErrMigrateInterference", err)
	}
	if b, err := os.ReadFile(snap + ".tmp"); err != nil || string(b) != "partial" { //nolint:gosec // G304: the test's own file
		t.Fatalf("the temp was removed before the refusal: %q, %v", b, err)
	}
}

// markStarted's late check (a defence: checkPlanted refuses first) reports late interference
// once.
func TestMarkStartedRefusesAFolderOnce(t *testing.T) {
	db, dir := newDB(t)
	in := intent{From: 1, To: 2, Snapshot: snapshotName(1, 2)}
	mustMkdir(t, filepath.Join(dir, markerName(1, 2)))
	assertLate(t, markStarted(context.Background(), db, in))
}

// writeIntent never replaces an intent already in place (RENAME_NOREPLACE): one that
// appeared is interference, left as it was, and the temp is removed.
func TestWriteIntentNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, MigrateIntent), []byte("someone else's"))
	err := writeIntent(dir, intent{From: 1, To: 2, Snapshot: snapshotName(1, 2)})
	if !errors.Is(err, ErrMigrateInterference) {
		t.Fatalf("got %v, want ErrMigrateInterference", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, MigrateIntent)); err != nil || string(b) != "someone else's" { //nolint:gosec // G304: the test's own file
		t.Fatalf("the intent was replaced: %q, %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, MigrateIntent+".tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the intent's temp is left: %v", err)
	}
}
