//go:build linux

package sql

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var rebuildNow = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

const rebuildStamp = "20261117T100000Z"

// walDB is a state directory whose app.db has a committed row only in its WAL ("wal-only"),
// as a killed server leaves it: app.db, -wal and -shm all move.
func walDB(t *testing.T) string {
	t.Helper()
	db, src := newDB(t)
	for _, s := range []string{"PRAGMA wal_autocheckpoint = 0", "INSERT INTO meta (key, value) VALUES ('wal-only', '1')"} {
		if _, err := db.w.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "state")
	must(t, os.Mkdir(dir, 0o700))
	for _, n := range dbFiles[:3] {
		b, err := os.ReadFile(filepath.Join(src, n)) //nolint:gosec // G304: the test's own temp folder
		must(t, err)
		must(t, os.WriteFile(filepath.Join(dir, n), b, 0o600)) //nolint:gosec // G703: the test's own temp folder
	}
	return dir
}

func exists(dir, n string) bool { _, err := os.Lstat(filepath.Join(dir, n)); return err == nil }

func begin(t *testing.T, dir string) Rebuild {
	t.Helper()
	r, err := BeginRebuild(context.Background(), dir, rebuildNow)
	must(t, err)
	return r
}

// TestOpenRefusesWhileARebuildIsInterrupted: rebuild.intent, a file or anything else at its
// name, stops every open but the rebuild's own, and OpenExisting creates nothing where
// app.db is missing.
func TestOpenRefusesWhileARebuildIsInterrupted(t *testing.T) {
	ctx := context.Background()
	folder := closedDB(t)
	mustMkdir(t, filepath.Join(folder, RebuildIntent))
	if _, err := Open(ctx, folder, discard); !errors.Is(err, ErrRebuildInterrupted) {
		t.Fatalf("Open beside a rebuild.intent folder = %v, want ErrRebuildInterrupted", err)
	}

	dir := closedDB(t)
	r := begin(t, dir)
	if _, err := Open(ctx, dir, discard); !errors.Is(err, ErrRebuildInterrupted) {
		t.Fatalf("Open = %v, want ErrRebuildInterrupted", err)
	}
	if _, err := MoveAsideForRebuild(ctx, dir, r); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(ctx, dir, discard); !errors.Is(err, ErrNoDatabase) || exists(dir, AppDB) {
		t.Fatalf("OpenExisting = %v; app.db there: %v", err, exists(dir, AppDB))
	}
	db, err := OpenForRebuild(ctx, dir, discard)
	must(t, err)
	must(t, db.Close())
	must(t, FinishRebuild(dir))
	db, err = Open(ctx, dir, discard)
	must(t, err)
	must(t, db.Close())
}

// TestBeginRebuildRefuses: rollback.intent, app.db that isn't a regular file, or a damaged
// folder already there for that second, refuses with nothing written; so does an
// interrupted migration that db rollback can undo.
func TestBeginRebuildRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{"rollback.intent", func(t *testing.T) string {
			dir := closedDB(t)
			writeFile(t, filepath.Join(dir, RollbackIntent), []byte("{}"))
			return dir
		}},
		{"app.db a folder", func(t *testing.T) string {
			dir := closedDB(t)
			mustRemove(t, filepath.Join(dir, AppDB))
			mustMkdir(t, filepath.Join(dir, AppDB))
			return dir
		}},
		{"the damaged folder", func(t *testing.T) string {
			dir := closedDB(t)
			mustMkdir(t, filepath.Join(dir, "damaged-"+rebuildStamp))
			return dir
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.setup(t)
			if _, err := BeginRebuild(context.Background(), dir, rebuildNow); !errors.Is(err, ErrRebuildRefused) {
				t.Fatalf("BeginRebuild = %v, want ErrRebuildRefused", err)
			}
			if exists(dir, RebuildIntent) || exists(dir, RebuildIntent+".tmp") {
				t.Fatal("an intent was written")
			}
		})
	}
	t.Run("a migration db rollback can undo", func(t *testing.T) {
		dir, fsys, _ := failedMigration(t)
		if _, err := beginRebuild(context.Background(), dir, rebuildNow, fsys); !errors.Is(err, ErrRebuildRefused) ||
			!bytes.Contains([]byte(err.Error()), []byte("db rollback")) {
			t.Fatalf("beginRebuild = %v, want a refusal naming db rollback", err)
		}
		if exists(dir, RebuildIntent) {
			t.Fatal("an intent was written")
		}
	})
}

// TestReadRebuildIntentRefusesAnotherFile: an intent this build couldn't have written is
// interference.
func TestReadRebuildIntentRefusesAnotherFile(t *testing.T) {
	for _, b := range []string{`{}`, `{"uuid":"x","time":"2026-11-17T10:00:00Z","had_db":true}`,
		`{"uuid":"6f1c2a7e-3b4d-4c5e-8f90-a1b2c3d4e5f6","time":"yesterday","had_db":true}`,
		`{"uuid":"6f1c2a7e-3b4d-4c5e-8f90-a1b2c3d4e5f6","time":"2026-11-17T10:00:00Z","had_db":true,"x":1}`} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, RebuildIntent), []byte(b))
		if _, err := ReadRebuildIntent(dir); !errors.Is(err, ErrRebuildInterference) {
			t.Fatalf("ReadRebuildIntent(%s) = %v, want interference", b, err)
		}
	}
}

// TestKeepRecovered: a temp is kept once, streamed; the same bytes again are the copy a
// crash left; other bytes are interference; a name with a slash is refused.
func TestKeepRecovered(t *testing.T) {
	dir := t.TempDir()
	r := Rebuild{UUID: NewUUID(), Time: "2026-11-17T10:00:00Z"}
	keep := func(name, s string) error { return KeepRecovered(dir, r, name, bytes.NewReader([]byte(s))) }
	must(t, keep("save-1", "draft"))
	must(t, keep("save-1", "draft"))
	b, err := os.ReadFile(filepath.Join(dir, r.RecoveryDir(), "save-1")) //nolint:gosec // G304: the test's own folder
	if err != nil || string(b) != "draft" {
		t.Fatalf("kept %q, %v", b, err)
	}
	for _, other := range []string{"other", "draft and more", "dra"} {
		if err := keep("save-1", other); !errors.Is(err, ErrRebuildInterference) {
			t.Fatalf("other bytes %q: %v, want interference", other, err)
		}
	}
	if err := keep("../x", ""); err == nil {
		t.Fatal("a name with a slash was kept")
	}
	if fi, err := os.Stat(filepath.Join(dir, r.RecoveryDir())); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the recovery folder: %v, %v", fi, err)
	}
}
