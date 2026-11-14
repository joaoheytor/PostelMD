//go:build linux

package sql

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestRollbackHelper runs db rollback on -sql.crash-dir with failingMigrations and is
// SIGKILLed at -sql.crash-at (CLAUDE.md rule 14).
func TestRollbackHelper(t *testing.T) {
	if *crashDir == "" {
		t.Skip("run by TestRollbackCrashPoints")
	}
	fsys, _ := failingMigrations(t)
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
	r, err := rollback(context.Background(), *crashDir, fsys, rollbackNow)
	t.Fatalf("not killed at %s (rollback: %+v, %v)", *crashAt, r, err)
}

func runRollbackHelper(t *testing.T, dir, at string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRollbackHelper$", "-test.count=1", //nolint:gosec // G204: this test binary, with its own flags
		"-sql.crash-dir="+dir, "-sql.crash-at="+at)
	return cmd.CombinedOutput()
}

// rbDisk is what a crash in db rollback left: both intents, the rollback folder, the moved
// database, the database and its copy in the state directory, the snapshot in either place,
// and whether a WAL is still in the state directory.
type rbDisk struct {
	rb, migrate, dir, moved, db, tmp, snap, snapMoved, wal bool
}

func readRBDisk(t *testing.T, dir string, from int) rbDisk {
	t.Helper()
	exists := func(n string) bool { _, err := os.Lstat(filepath.Join(dir, n)); return err == nil }
	d, snap := rollbackDirName(rollbackNow), snapshotName(from, from+2)
	return rbDisk{
		rb: exists(RollbackIntent), migrate: exists(MigrateIntent), dir: exists(d),
		moved: exists(filepath.Join(d, AppDB)), db: exists(AppDB), tmp: exists(AppDB + ".tmp"),
		snap: exists(snap), snapMoved: exists(filepath.Join(d, snap)), wal: exists(AppDB + "-wal"),
	}
}

// Recovery table of db rollback (ADR-0035), from a failed migration whose process was killed
// (a WAL with a committed row beside app.db, so three files move). Each row is the state a
// kill leaves; the next run of db rollback must finish it, and the previous build must then
// open the restored database:
//
//	| Killed at                     | Left                                          | Next run              |
//	|-------------------------------|-----------------------------------------------|-----------------------|
//	| rollback-intent-synced        | the intent's temp only                        | checks again, starts  |
//	| rollback-intent-written       | the intent                                    | makes the folder, on  |
//	| rollback-dir-made             | the intent, the empty folder                  | moves, on             |
//	| rollback-moved (1st)          | app.db moved; -wal, -shm still in place       | moves the rest, on    |
//	| rollback-moved (2nd, 3rd)     | -wal, then -shm moved too                     | moves the rest, on    |
//	| rollback-moved-synced         | all three moved                               | copies, on            |
//	| rollback-copied, -verified    | app.db.tmp                                    | copies again, on      |
//	| rollback-restored, -synced    | app.db is the snapshot's copy                 | same bytes: moves the |
//	|                               |                                               | snapshot, on          |
//	| rollback-snapshot-moved       | the snapshot in the folder                    | retires the intent    |
//	| rollback-retired              | migrate.intent gone                           | removes its intent    |
//	| rollback-done                 | both intents gone                             | nothing to roll back  |
func TestRollbackCrashPoints(t *testing.T) {
	start := rbDisk{migrate: true, db: true, snap: true, wal: true}
	in := rbDisk{rb: true, migrate: true, db: true, snap: true, wal: true}
	folder := rbDisk{rb: true, migrate: true, dir: true, db: true, snap: true, wal: true}
	first := rbDisk{rb: true, migrate: true, dir: true, moved: true, snap: true, wal: true}
	moved := rbDisk{rb: true, migrate: true, dir: true, moved: true, snap: true}
	copied := rbDisk{rb: true, migrate: true, dir: true, moved: true, tmp: true, snap: true}
	restored := rbDisk{rb: true, migrate: true, dir: true, moved: true, db: true, snap: true}
	snapMoved := rbDisk{rb: true, migrate: true, dir: true, moved: true, db: true, snapMoved: true}
	retired := rbDisk{rb: true, dir: true, moved: true, db: true, snapMoved: true}
	done := rbDisk{dir: true, moved: true, db: true, snapMoved: true}
	for _, c := range []struct {
		at   string
		want rbDisk
	}{
		{"rollback-intent-synced:1", start}, {"rollback-intent-written:1", in}, {"rollback-dir-made:1", folder},
		{"rollback-moved:1", first}, {"rollback-moved:2", moved}, {"rollback-moved:3", moved},
		{"rollback-moved-synced:1", moved}, {"rollback-copied:1", copied}, {"rollback-verified:1", copied},
		{"rollback-restored:1", restored}, {"rollback-restored-synced:1", restored},
		{"rollback-snapshot-moved:1", snapMoved}, {"rollback-retired:1", retired}, {"rollback-done:1", done},
	} {
		t.Run(c.at, func(t *testing.T) {
			dir, fsys, from := staleWAL(t)
			if out, err := runRollbackHelper(t, dir, c.at); !killed(err) {
				t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
			}
			if got := readRBDisk(t, dir, from); got != c.want {
				t.Fatalf("after the crash %+v, want %+v", got, c.want)
			}
			r, err := rollback(context.Background(), dir, fsys, rollbackNow)
			if c.want == done {
				if !errors.Is(err, ErrRollbackRefused) || !strings.Contains(err.Error(), "nothing to roll back") {
					t.Fatalf("the next run = %v, want nothing to roll back", err)
				}
				r = RolledBack{From: from, To: from + 2, Dir: rollbackDirName(rollbackNow), Restored: true}
			} else if err != nil {
				t.Fatalf("the next run: %v", err)
			}
			if !hasRow(t, filepath.Join(dir, r.Dir, AppDB), "wal-only") {
				t.Fatal("the moved database lost the WAL's row")
			}
			assertRolledBack(t, dir, r, from, from+2)
		})
	}
}

// The crash points of a rollback with no snapshot to restore (the migration was interrupted
// while copying it): the temp goes, then migrate.intent.
//
//	| Killed at         | Left                         | Next run                              |
//	|-------------------|------------------------------|---------------------------------------|
//	| rollback-cleaned  | migrate.intent, no temp      | retires the intent                    |
//	| rollback-retired  | nothing                      | nothing to roll back                  |
func TestRollbackRetireCrashPoints(t *testing.T) {
	for _, c := range []struct {
		at      string
		migrate bool
	}{{"rollback-cleaned:1", true}, {"rollback-retired:1", false}} {
		t.Run(c.at, func(t *testing.T) {
			dir := closedDB(t)
			fsys, from := failingMigrations(t)
			if out, err := runHelper(t, dir, "-sql.crash-at=copied:1"); !killed(err) {
				t.Fatalf("the migration helper wasn't killed: %v\n%s", err, out)
			}
			if out, err := runRollbackHelper(t, dir, c.at); !killed(err) {
				t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
			}
			if got := readOnDisk(t, dir, from, from+2); got != (onDisk{intent: c.migrate}) {
				t.Fatalf("after the crash %+v", got)
			}
			_, err := rollback(context.Background(), dir, fsys, rollbackNow)
			if c.migrate && err != nil {
				t.Fatalf("the next run: %v", err)
			}
			if !c.migrate && !errors.Is(err, ErrRollbackRefused) {
				t.Fatalf("the next run = %v, want nothing to roll back", err)
			}
			assertRetired(t, dir, from)
		})
	}
}
