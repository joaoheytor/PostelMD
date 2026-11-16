//go:build linux

package sql

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"
)

// assigned is a database with anAssignment's rows, and their rows by path.
func assigned(t *testing.T) (*DB, map[string]File) {
	t.Helper()
	db, _ := newDB(t)
	if err := db.Assign(context.Background(), anAssignment(time.Now())); err != nil {
		t.Fatal(err)
	}
	return db, rowsByPath(t, db)
}

func rowsByPath(t *testing.T, db *DB) map[string]File {
	t.Helper()
	files, err := db.Files(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]File{}
	for _, f := range files {
		out[f.Path] = f
	}
	return out
}

func rowOf(f File) Row { return Row{ID: f.ID, Path: f.Path, Generation: f.Generation} }

// aPass is one unit of each kind: a.md moves to moved.md (bumped), notes/b.md is held for
// by held.md as a rule-2 match, the unhashed file is tombstoned, and new.md gets an ID.
func aPass(rows map[string]File) [][]Change {
	a, b, u := rows["a.md"], rows["notes/b.md"], rows["notes/\xff\xfe.md"]
	return [][]Change{
		{UpdateFile{Row: rowOf(a), NewPath: "moved.md", SHA256: a.SHA256, Stat: a.Stat, Bump: true}},
		{Suspect{Row: rowOf(b)}, Hold{Path: "held.md", FileID: b.ID, SHA256: b.SHA256, Stat: FileStat{Size: 2, Inode: 99}}},
		{TombstoneFile{Row: rowOf(u), Reason: "missing"}},
		{InsertFile{Path: "new.md", SHA256: sum(7), Stat: FileStat{Size: 7, Inode: 70}}},
	}
}

// checkPassApplied asserts every change of aPass is in db.
func checkPassApplied(t *testing.T, db *DB, before map[string]File, at time.Time) {
	t.Helper()
	ctx := context.Background()
	rows := rowsByPath(t, db)
	if m := rows["moved.md"]; m.ID != before["a.md"].ID || m.Generation != 2 || m.Suspect {
		t.Fatalf("moved.md: %+v", m)
	}
	if b := rows["notes/b.md"]; !b.Suspect || !b.SuspectSince.Equal(at.Truncate(time.Millisecond)) {
		t.Fatalf("notes/b.md: %+v, want suspect since %v", b, at)
	}
	if h, err := db.Holds(ctx); err != nil || len(h) != 1 || h[0].Path != "held.md" || h[0].FileID != before["notes/b.md"].ID {
		t.Fatalf("holds %+v, %v", h, err)
	}
	u := before["notes/\xff\xfe.md"]
	if ts, err := db.TombstoneByID(ctx, u.ID); err != nil || ts.LastPath != u.Path || ts.LastGeneration != 1 || ts.Reason != "missing" {
		t.Fatalf("tombstone %+v, %v", ts, err)
	}
	if n := rows["new.md"]; !ValidID(n.ID) || n.Generation != 1 || !bytes.Equal(n.SHA256, sum(7)) {
		t.Fatalf("new.md: %+v", n)
	}
	if len(rows) != 3 { // moved.md, notes/b.md, new.md
		t.Fatalf("rows %v", rows)
	}
}

func TestApplyScan(t *testing.T) {
	db, before := assigned(t)
	now := time.Now()
	applied, err := db.ApplyScan(context.Background(), now, aPass(before))
	if err != nil || len(applied) != 4 || !applied[0] || !applied[1] || !applied[2] || !applied[3] {
		t.Fatalf("ApplyScan = %v, %v", applied, err)
	}
	checkPassApplied(t, db, before, now)
}

// A unit whose row changed since the scan read it is rolled back alone; the rest commits.
func TestApplyScanSkipsAStaleUnit(t *testing.T) {
	db, before := assigned(t)
	ctx := context.Background()
	a, b := before["a.md"], before["notes/b.md"]
	stale := rowOf(b)
	stale.Generation = 7
	units := [][]Change{
		{UpdateFile{Row: rowOf(a), NewPath: "a.md", SHA256: sum(9), Stat: a.Stat, Bump: true}},
		// The stale change comes second: the unit's first change must be undone too.
		{InsertFile{Path: "x.md", SHA256: sum(8)}, Suspect{Row: stale}},
		{InsertFile{Path: "a.md", SHA256: sum(8)}},                                   // a.md has a row
		{Hold{Path: "notes/b.md", FileID: a.ID, SHA256: sum(8)}},                     // the path has a row
		{Hold{Path: "free.md", FileID: b.ID, SHA256: sum(8)}},                        // the row isn't suspect
		{UpdateFile{Row: rowOf(b), NewPath: "a.md", SHA256: b.SHA256, Stat: b.Stat}}, // an ID moving onto a row
		{UpdateFile{Row: stale, NewPath: "notes/b.md", SHA256: sum(8), Stat: b.Stat}},
		{TombstoneFile{Row: stale, Reason: "missing"}},
	}
	applied, err := db.ApplyScan(ctx, time.Now(), units)
	if err != nil || !applied[0] || slices.Contains(applied[1:], true) {
		t.Fatalf("ApplyScan = %v, %v; want only the first unit", applied, err)
	}
	rows := rowsByPath(t, db)
	if rows["a.md"].Generation != 2 || rows["notes/b.md"].Suspect || rows["notes/b.md"].Generation != 1 || len(rows) != 3 {
		t.Fatalf("rows after the pass: %+v", rows)
	}
	if _, err := db.TombstoneByID(ctx, b.ID); !errors.Is(err, ErrNoTombstone) {
		t.Fatalf("a stale tombstone was recorded: %v", err)
	}
	if h, _ := db.Holds(ctx); len(h) != 0 {
		t.Fatalf("holds %+v", h)
	}
}

// A unit made from an older view of a suspect is stale too: marking a row that is already
// suspect (its clock would restart), and holding a second path for a row that holds one.
func TestApplyScanSkipsAStaleSuspectOrHold(t *testing.T) {
	db, before := assigned(t)
	ctx := context.Background()
	b := before["notes/b.md"]
	t0 := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	first := [][]Change{{Suspect{Row: rowOf(b)}, Hold{Path: "held.md", FileID: b.ID, SHA256: b.SHA256}}}
	if applied, err := db.ApplyScan(ctx, t0, first); err != nil || !applied[0] {
		t.Fatalf("ApplyScan = %v, %v", applied, err)
	}
	units := [][]Change{
		{Suspect{Row: rowOf(b)}},
		{Hold{Path: "other.md", FileID: b.ID, SHA256: b.SHA256}},
		{InsertFile{Path: "x.md", SHA256: sum(8)}},
	}
	applied, err := db.ApplyScan(ctx, t0.Add(time.Minute), units)
	if err != nil || applied[0] || applied[1] || !applied[2] {
		t.Fatalf("ApplyScan = %v, %v; want only the insert", applied, err)
	}
	rows := rowsByPath(t, db)
	if r := rows["notes/b.md"]; !r.Suspect || !r.SuspectSince.Equal(t0) {
		t.Fatalf("notes/b.md: %+v, want suspect since %v", r, t0)
	}
	if h, _ := db.Holds(ctx); len(h) != 1 || h[0].Path != "held.md" {
		t.Fatalf("holds %+v", h)
	}
	if _, ok := rows["x.md"]; !ok {
		t.Fatal("the unit after the stale ones wasn't applied")
	}
}

func TestExpireTombstones(t *testing.T) {
	db, before := assigned(t)
	ctx := context.Background()
	old, recent := before["a.md"], before["notes/b.md"]
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i, f := range []File{old, recent} {
		if _, err := db.ApplyScan(ctx, t0.Add(time.Duration(i)*time.Hour), [][]Change{{TombstoneFile{Row: rowOf(f), Reason: "missing"}}}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := db.ExpireTombstones(ctx, t0.Add(time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("ExpireTombstones = %d, %v; want 1", n, err)
	}
	if _, err := db.TombstoneByID(ctx, old.ID); !errors.Is(err, ErrNoTombstone) {
		t.Fatalf("the old tombstone: %v", err)
	}
	if _, err := db.TombstoneByID(ctx, recent.ID); err != nil {
		t.Fatalf("the recent tombstone: %v", err)
	}
	// The boundary: a tombstone recorded at the cutoff is kept; a millisecond later it goes.
	if n, err := db.ExpireTombstones(ctx, t0.Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("ExpireTombstones at the tombstone's time = %d, %v; want 0", n, err)
	}
	if n, err := db.ExpireTombstones(ctx, t0.Add(time.Hour+time.Millisecond)); err != nil || n != 1 {
		t.Fatalf("ExpireTombstones a millisecond later = %d, %v; want 1", n, err)
	}
}

// TestApplyScanHelper applies aPass to the database in -sql.crash-dir, SIGKILLed at the
// kill point -sql.crash-at names.
func TestApplyScanHelper(t *testing.T) {
	if *crashDir == "" || *crashAt == "" {
		t.Skip("run by TestApplyScanCrashPoints")
	}
	db, err := Open(context.Background(), *crashDir, discard)
	if err != nil {
		t.Fatal(err)
	}
	killPoint = func(p string) {
		if p == *crashAt {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {} // the signal is on its way
		}
	}
	_, err = db.ApplyScan(context.Background(), time.Now(), aPass(rowsByPath(t, db)))
	t.Fatalf("not killed at %s (ApplyScan: %v)", *crashAt, err)
}

// A scan pass is one transaction (ADR-0036):
//
//	| Killed at           | In app.db                       | Next start                         |
//	|---------------------|---------------------------------|------------------------------------|
//	| scan-before-commit  | the rows as before the pass     | the next pass plans it again       |
//	| scan-after-commit   | every unit of the pass          | the next pass goes on from there   |
func TestApplyScanCrashPoints(t *testing.T) {
	for _, c := range []struct {
		at        string
		committed bool
	}{{"scan-before-commit", false}, {"scan-after-commit", true}} {
		t.Run(c.at, func(t *testing.T) {
			db, dir := newDB(t)
			if err := db.Assign(context.Background(), anAssignment(time.Now())); err != nil {
				t.Fatal(err)
			}
			before := rowsByPath(t, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			cmd := exec.Command(os.Args[0], "-test.run=^TestApplyScanHelper$", "-test.count=1", //nolint:gosec // G204: this test binary, with its own flags
				"-sql.crash-at="+c.at, "-sql.crash-dir="+dir)
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
			}
			db, err = Open(context.Background(), dir, discard)
			if err != nil {
				t.Fatalf("the next open: %v", err)
			}
			defer func() { _ = db.Close() }()
			if !c.committed {
				after := rowsByPath(t, db)
				for p, f := range before {
					if g := after[p]; g.ID != f.ID || g.Generation != f.Generation || g.Suspect || len(after) != len(before) {
						t.Fatalf("killed before the commit, but %q is %+v (was %+v)", p, g, f)
					}
					if _, err := db.TombstoneByID(context.Background(), f.ID); !errors.Is(err, ErrNoTombstone) {
						t.Fatalf("killed before the commit, but %q has a tombstone: %v", p, err)
					}
				}
				if h, err := db.Holds(context.Background()); err != nil || len(h) != 0 {
					t.Fatalf("killed before the commit, but holds %+v, %v", h, err)
				}
				return
			}
			b := rowsByPath(t, db)["notes/b.md"]
			if b.SuspectSince.Before(start.Truncate(time.Millisecond)) {
				t.Fatalf("suspect since %v, before the helper started", b.SuspectSince)
			}
			checkPassApplied(t, db, before, b.SuspectSince)
		})
	}
}
