//go:build linux

package sql

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestMoveAsideForRebuild: the original with its WAL files goes to the damaged folder, which
// then records that move; each half-built database goes to the next attempt-<n>/; a built
// one is reported, and refused when the moved database is gone.
func TestMoveAsideForRebuild(t *testing.T) {
	ctx := context.Background()
	dir := walDB(t)
	r := begin(t, dir)
	d := filepath.Join(dir, r.DamagedDir())
	if built, err := MoveAsideForRebuild(ctx, dir, r); err != nil || built {
		t.Fatalf("MoveAsideForRebuild = %v, %v", built, err)
	}
	for _, n := range dbFiles[:3] {
		if exists(dir, n) || !exists(d, n) {
			t.Fatalf("%s didn't move", n)
		}
	}
	if !exists(d, movedMarker) {
		t.Fatal("the damaged folder doesn't record the original's move")
	}
	if !hasRow(t, filepath.Join(d, AppDB), "wal-only") {
		t.Fatal("the moved database lost its WAL's row")
	}
	// An attempt folder made before a crash, with nothing in it, is used again; then two
	// half-built databases.
	mustMkdir(t, filepath.Join(d, "attempt-1"))
	for _, a := range []string{"attempt-1", "attempt-2"} {
		db, err := OpenForRebuild(ctx, dir, discard)
		must(t, err)
		must(t, db.Close())
		if built, err := MoveAsideForRebuild(ctx, dir, r); err != nil || built {
			t.Fatalf("%s: %v, %v", a, built, err)
		}
		if !exists(d, filepath.Join(a, AppDB)) {
			t.Fatalf("%s/app.db is missing", a)
		}
	}
	db, err := OpenForRebuild(ctx, dir, discard)
	must(t, err)
	must(t, db.Assign(ctx, Assignment{VaultUUID: NewUUID(), RebuildUUID: r.UUID, Now: rebuildNow}))
	must(t, db.Close())
	if built, err := MoveAsideForRebuild(ctx, dir, r); err != nil || !built {
		t.Fatalf("a built database: %v, %v", built, err)
	}
	must(t, os.Rename(filepath.Join(d, AppDB), filepath.Join(dir, "elsewhere")))
	if _, err := MoveAsideForRebuild(ctx, dir, r); !errors.Is(err, ErrRebuildInterference) {
		t.Fatalf("the moved database gone: %v, want interference", err)
	}
}

// TestAnEarlierRebuildsDatabaseIsMoved: an app.db made by an earlier db rebuild carries
// that rebuild's UUID, not this one's: it is the original, and moves.
func TestAnEarlierRebuildsDatabaseIsMoved(t *testing.T) {
	ctx := context.Background()
	db, dir := newDB(t)
	must(t, db.Assign(ctx, Assignment{VaultUUID: NewUUID(), RebuildUUID: NewUUID(), Now: rebuildNow}))
	must(t, db.Close())
	r := begin(t, dir)
	if built, err := MoveAsideForRebuild(ctx, dir, r); err != nil || built {
		t.Fatalf("MoveAsideForRebuild = %v, %v; want the original moved", built, err)
	}
	if exists(dir, AppDB) || !exists(filepath.Join(dir, r.DamagedDir()), AppDB) {
		t.Fatal("the earlier rebuild's database didn't move")
	}
}

// TestStaleWALWithoutADatabase: app.db lost, its -wal and -shm left. They move first (the
// original's move); the half-built database a crash then leaves, with its own -wal and
// -shm, goes to attempt-1/, never into the damaged folder beside them.
func TestStaleWALWithoutADatabase(t *testing.T) {
	ctx := context.Background()
	dir := walDB(t)
	mustRemove(t, filepath.Join(dir, AppDB))
	r := begin(t, dir)
	if r.HadDB {
		t.Fatal("had_db with no app.db")
	}
	if _, err := MoveAsideForRebuild(ctx, dir, r); err != nil {
		t.Fatal(err)
	}
	db, err := OpenForRebuild(ctx, dir, discard) // left open: a crash keeps its -wal and -shm
	must(t, err)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.w.Exec("PRAGMA wal_autocheckpoint = 0"); err != nil {
		t.Fatal(err)
	}
	if built, err := MoveAsideForRebuild(ctx, dir, r); err != nil || built {
		t.Fatalf("the half-built database: %v, %v", built, err)
	}
	a := filepath.Join(dir, r.DamagedDir(), "attempt-1")
	for _, n := range dbFiles[:3] {
		if exists(dir, n) || !exists(a, n) {
			t.Fatalf("%s: in the state directory %v, in attempt-1 %v", n, exists(dir, n), exists(a, n))
		}
	}
}

// TestMoveAsideRefusesAnUnreadableDatabase: an app.db beside the intent that can't be
// opened isn't taken for a half-built one: the move stops with the error and moves nothing.
// A file that isn't a database is half-built, and moves.
func TestMoveAsideRefusesAnUnreadableDatabase(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file without read permission")
	}
	ctx := context.Background()
	dir := closedDB(t)
	r := begin(t, dir)
	if _, err := MoveAsideForRebuild(ctx, dir, r); err != nil {
		t.Fatal(err)
	}
	db, err := OpenForRebuild(ctx, dir, discard)
	must(t, err)
	must(t, db.Close())
	must(t, os.Chmod(filepath.Join(dir, AppDB), 0))
	if _, err := MoveAsideForRebuild(ctx, dir, r); err == nil || !exists(dir, AppDB) {
		t.Fatalf("an unreadable app.db: %v, still there %v; want an error and nothing moved", err, exists(dir, AppDB))
	}
	must(t, os.Remove(filepath.Join(dir, AppDB)))
	writeFile(t, filepath.Join(dir, AppDB), []byte("not a database, and long enough to be read as a header....................................................."))
	if built, err := MoveAsideForRebuild(ctx, dir, r); err != nil || built || exists(dir, AppDB) {
		t.Fatalf("a file that isn't a database: %v, %v; want it moved", built, err)
	}
}

// TestMoveAsideRefusesWhenTheOriginalIsLost: the intent says app.db existed, but it is in
// neither the state directory nor the damaged folder.
func TestMoveAsideRefusesWhenTheOriginalIsLost(t *testing.T) {
	dir := closedDB(t)
	r := begin(t, dir)
	must(t, os.Rename(filepath.Join(dir, AppDB), filepath.Join(t.TempDir(), "gone")))
	if _, err := MoveAsideForRebuild(context.Background(), dir, r); !errors.Is(err, ErrRebuildInterference) {
		t.Fatalf("MoveAsideForRebuild = %v, want interference", err)
	}
}
