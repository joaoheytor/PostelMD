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

// A copy swapped while it was written (Snapshot's ErrCopyReplaced) is late interference too,
// and the file put in its place is left as it was.
func TestTakeSnapshotReplacedIsLate(t *testing.T) {
	db, dir := newDB(t)
	snap := filepath.Join(dir, "pre-migrate.db")
	other := filepath.Join(dir, "other")
	writeFile(t, other, []byte("someone else's"))
	setKillPoint(t, func(p string) {
		if p == "copied" {
			if err := os.Rename(other, snap+".tmp"); err != nil {
				t.Error(err)
			}
		}
	})
	err := takeSnapshot(context.Background(), db, snap, len(embeddedFS(t)))
	assertLate(t, err)
	if !errors.Is(err, ErrCopyReplaced) {
		t.Fatalf("got %v, want ErrCopyReplaced too", err)
	}
	if b, err := os.ReadFile(snap + ".tmp"); err != nil || string(b) != "someone else's" { //nolint:gosec // G304: the test's own file
		t.Fatalf("the file put at the temp: %q, %v", b, err)
	}
}

// A fresh migration's refusals after its intent is written are late, whatever step finds
// them, and the intent stays: a file planted once the intent is in place is refused by
// resume's checks (checkPlanted, verifySnapshot), never with "nothing was changed".
func TestRefusalAfterTheIntentIsLate(t *testing.T) {
	for _, c := range []struct{ name, suffix string }{
		{"a file at the snapshot", ""},
		{"a file at the snapshot temp's -wal", ".tmp-wal"},
		{"a file at the snapshot's -journal", "-journal"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := closedDB(t)
			fsys, from := laterMigrations(t, 2)
			snap := filepath.Join(dir, snapshotName(from, from+2))
			setKillPoint(t, func(p string) {
				if p == "intent-written" {
					// open runs in openGuarded's goroutine: t.Error, not t.Fatal.
					if err := os.WriteFile(snap+c.suffix, []byte("planted"), 0o600); err != nil {
						t.Error(err)
					}
				}
			})
			db, err := openGuarded(t, dir, fsys)
			if db != nil {
				_ = db.Close()
			}
			assertLate(t, err)
			if _, err := os.Lstat(filepath.Join(dir, MigrateIntent)); err != nil {
				t.Fatalf("the intent is gone: %v", err)
			}
			if v := committedVersion(t, dir); v != from {
				t.Fatalf("a refused open migrated to %d", v)
			}
		})
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
