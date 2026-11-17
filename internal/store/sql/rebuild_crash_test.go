//go:build linux

package sql

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

var rebuildSeq = flag.String("sql.rebuild-seq", "", "what the rebuild crash helper runs: steps, attempt, keep or upgrade")

// rebuildSteps runs db rebuild's state-directory steps as internal/ids does: the intent (or
// the one there) and the move; with attempt, a half-built database (left open, as a crash
// leaves it, with its -wal and -shm) and its move too.
func rebuildSteps(dir string, attempt bool) (Rebuild, error) {
	ctx := context.Background()
	r, err := ReadRebuildIntent(dir)
	if err != nil {
		return Rebuild{}, err
	}
	if r == nil {
		b, err := BeginRebuild(ctx, dir, rebuildNow)
		if err != nil {
			return b, err
		}
		r = &b
	}
	if _, err := MoveAsideForRebuild(ctx, dir, *r); err != nil || !attempt {
		return *r, err
	}
	db, err := OpenForRebuild(ctx, dir, discard)
	if err != nil {
		return *r, err
	}
	if _, err := db.w.Exec("PRAGMA wal_autocheckpoint = 0"); err != nil {
		return *r, err
	}
	_, err = MoveAsideForRebuild(ctx, dir, *r)
	return *r, err
}

// TestRebuildStepsHelper runs -sql.rebuild-seq on -sql.crash-dir and is SIGKILLed at
// -sql.crash-at (CLAUDE.md rule 14).
func TestRebuildStepsHelper(t *testing.T) {
	if *crashDir == "" || *rebuildSeq == "" {
		t.Skip("run by TestRebuildCrashPoints")
	}
	name, nth, _ := strings.Cut(*crashAt, ":")
	want, _ := strconv.Atoi(nth)
	seen := 0
	killPoint = func(p string) {
		if p == name {
			if seen++; seen == want {
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
				select {} // the signal is on its way
			}
		}
	}
	var err error
	switch *rebuildSeq {
	case "upgrade": // the first upgrade's intent (ADR-0041), written then removed
		if _, err = BeginUpgrade(*crashDir, rebuildNow); err == nil {
			err = FinishUpgrade(*crashDir)
		}
	case "keep":
		err = KeepRecovered(*crashDir, Rebuild{UUID: NewUUID(), Time: "2026-11-17T10:00:00Z"}, "save-1",
			bytes.NewReader([]byte("draft")))
	default:
		var r Rebuild
		if r, err = rebuildSteps(*crashDir, *rebuildSeq == "attempt"); err == nil {
			err = FinishRebuild(*crashDir)
		}
		_ = r
	}
	t.Fatalf("not killed at %s (%v)", *crashAt, err)
}

func killRebuildSteps(t *testing.T, dir, seq, at string) {
	t.Helper()
	out, err := exec.Command(os.Args[0], "-test.run=^TestRebuildStepsHelper$", "-test.count=1", //nolint:gosec // G204: this test binary, with its own flags
		"-sql.crash-dir="+dir, "-sql.crash-at="+at, "-sql.rebuild-seq="+seq).CombinedOutput()
	if !killed(err) {
		t.Fatalf("the helper wasn't killed at %s: %v\n%s", at, err, out)
	}
}

// left is what a kill left in the state directory: the intent, its temp, which database
// files are still there, which moved, and whether the move is recorded.
type left struct {
	intent, tmp, recorded bool
	there, moved          string // of app.db, -wal, -shm: "d" for each one present, "-" else
}

func readLeft(dir string) left {
	d := filepath.Join(dir, "damaged-"+rebuildStamp)
	mark := func(in string) string {
		var b strings.Builder
		for _, n := range dbFiles[:3] {
			if exists(in, n) {
				b.WriteByte('d')
			} else {
				b.WriteByte('-')
			}
		}
		return b.String()
	}
	return left{intent: exists(dir, RebuildIntent), tmp: exists(dir, RebuildIntent+".tmp"),
		recorded: exists(d, movedMarker), there: mark(dir), moved: mark(d)}
}

// The crash points inside db rebuild's state-directory steps (ADR-0040); the steps between
// them are internal/ids's TestRebuildCrashPoints. "Left" is asserted right after each kill.
// From a database with a row only in its WAL (had_db), and from its -wal and -shm without
// app.db (had_db false):
//
//	| Killed at              | Left                                       | Next run                     |
//	|------------------------|--------------------------------------------|------------------------------|
//	| rebuild-intent-synced  | the intent's temp only                     | removes it, starts           |
//	| rebuild-intent-written | the intent; nothing moved                  | moves the files              |
//	| rebuild-moved (each)   | the files moved so far, not recorded       | moves the rest to the damaged |
//	|                        |                                            | folder, records the move     |
//	| rebuild-moved-recorded | all moved, the move recorded               | nothing more to move         |
//	| rebuild-done           | the intent unlinked, maybe not durably     | (no intent) nothing to do    |
func TestRebuildCrashPoints(t *testing.T) {
	type row struct {
		at   string
		want left
	}
	full := []row{
		{"rebuild-intent-synced:1", left{tmp: true, there: "ddd", moved: "---"}},
		{"rebuild-intent-written:1", left{intent: true, there: "ddd", moved: "---"}},
		{"rebuild-moved:1", left{intent: true, there: "-dd", moved: "d--"}},
		{"rebuild-moved:2", left{intent: true, there: "--d", moved: "dd-"}},
		{"rebuild-moved:3", left{intent: true, there: "---", moved: "ddd"}},
		{"rebuild-moved-recorded:1", left{intent: true, recorded: true, there: "---", moved: "ddd"}},
		{"rebuild-done:1", left{recorded: true, there: "---", moved: "ddd"}},
	}
	stale := []row{ // app.db lost: its -wal and -shm move
		{"rebuild-intent-written:1", left{intent: true, there: "-dd", moved: "---"}},
		{"rebuild-moved:1", left{intent: true, there: "--d", moved: "-d-"}},
		{"rebuild-moved-recorded:1", left{intent: true, recorded: true, there: "---", moved: "-dd"}},
	}
	for _, c := range []struct {
		name  string
		rows  []row
		noApp bool
	}{{"had_db", full, false}, {"stale WAL", stale, true}} {
		for _, rw := range c.rows {
			t.Run(c.name+"/"+rw.at, func(t *testing.T) {
				dir := walDB(t)
				if c.noApp {
					mustRemove(t, filepath.Join(dir, AppDB))
				}
				killRebuildSteps(t, dir, "steps", rw.at)
				if got := readLeft(dir); got != rw.want {
					t.Fatalf("after the kill %+v, want %+v", got, rw.want)
				}
				if rw.want.intent || rw.want.tmp {
					if _, err := rebuildSteps(dir, false); err != nil {
						t.Fatalf("the next run: %v", err)
					}
				}
				want := left{intent: true, recorded: true, there: "---", moved: rw.want.moved}
				if !c.noApp {
					want.moved = "ddd"
					if !hasRow(t, filepath.Join(dir, "damaged-"+rebuildStamp, AppDB), "wal-only") {
						t.Fatal("the moved database lost its WAL's row")
					}
				} else {
					want.moved = "-dd"
				}
				want.intent = exists(dir, RebuildIntent)
				if got := readLeft(dir); got != want {
					t.Fatalf("after the next run %+v, want %+v", got, want)
				}
			})
		}
	}
}

// TestAKillWhileAHalfBuiltDatabaseMoves: killed after a half-built database's app.db moved
// into attempt-1/ (the 4th move), its -wal and -shm follow it there on the next run; the
// damaged folder keeps the original's.
func TestAKillWhileAHalfBuiltDatabaseMoves(t *testing.T) {
	dir := walDB(t)
	killRebuildSteps(t, dir, "attempt", "rebuild-moved:4")
	d := filepath.Join(dir, "damaged-"+rebuildStamp)
	a := filepath.Join(d, "attempt-1")
	if !exists(a, AppDB) || exists(a, AppDB+"-wal") || !exists(dir, AppDB+"-wal") {
		t.Fatal("the kill didn't leave app.db moved and its -wal behind")
	}
	if _, err := rebuildSteps(dir, false); err != nil {
		t.Fatalf("the next run: %v", err)
	}
	for _, n := range dbFiles[:3] {
		if exists(dir, n) || !exists(a, n) || !exists(d, n) {
			t.Fatalf("%s: in the state directory %v, attempt-1 %v, damaged %v", n, exists(dir, n), exists(a, n), exists(d, n))
		}
	}
	if !hasRow(t, filepath.Join(d, AppDB), "wal-only") {
		t.Fatal("the original lost its WAL's row")
	}
}

// TestAKillBeforeAKeptTempIsInPlace: killed after the copy is synced, before its rename
// (rebuild-temp-copied), the copy is left beside the folder; the next run removes it and
// keeps the temp.
func TestAKillBeforeAKeptTempIsInPlace(t *testing.T) {
	dir := t.TempDir()
	killRebuildSteps(t, dir, "keep", "rebuild-temp-copied:1")
	copying := filepath.Join("recovery", ".rebuild-"+rebuildStamp+".copying")
	if !exists(dir, copying) || exists(dir, filepath.Join("recovery", "rebuild-"+rebuildStamp, "save-1")) {
		t.Fatal("the kill didn't leave the copy beside the folder")
	}
	r := Rebuild{UUID: NewUUID(), Time: "2026-11-17T10:00:00Z"}
	must(t, KeepRecovered(dir, r, "save-1", bytes.NewReader([]byte("draft"))))
	if exists(dir, copying) {
		t.Fatal("the leftover copy is still there")
	}
	b, err := os.ReadFile(filepath.Join(dir, r.RecoveryDir(), "save-1")) //nolint:gosec // G304: the test's own folder
	if err != nil || string(b) != "draft" {
		t.Fatalf("kept %q, %v", b, err)
	}
}
