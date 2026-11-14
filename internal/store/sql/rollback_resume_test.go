//go:build linux

package sql

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// While rollback.intent is there, Open refuses before creating or opening anything: this
// build would otherwise migrate a half-restored database again (ADR-0035).
func TestOpenRefusesAnInterruptedRollback(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"a rollback intent", func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, RollbackIntent), []byte("{}")) }},
		{"a rollback intent that is a folder", func(t *testing.T, dir string) { must(t, os.Mkdir(filepath.Join(dir, RollbackIntent), 0o700)) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			c.setup(t, dir)
			before := tree(t, dir)
			db, err := Open(context.Background(), dir, discard)
			if err == nil {
				_ = db.Close()
			}
			if !errors.Is(err, ErrRollbackInterrupted) {
				t.Fatalf("Open = %v, want ErrRollbackInterrupted", err)
			}
			if after := tree(t, dir); len(after) != len(before) {
				t.Fatalf("Open changed the state directory: %v", after)
			}
		})
	}
}

// A WAL, shared-memory or journal file that appears beside app.db after the moves and before
// the restored copy is renamed into place would be replayed into it: refused, nothing at
// app.db, the intent kept.
func TestRollbackRefusesAWALBesideTheRestoredFile(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			dir, fsys, _ := failedMigration(t)
			setKillPoint(t, func(p string) {
				if p == "rollback-verified" {
					writeFile(t, filepath.Join(dir, AppDB+suffix), []byte("stale"))
				}
			})
			_, err := rollback(context.Background(), dir, fsys, rollbackNow)
			if !errors.Is(err, ErrRollbackInterference) {
				t.Fatalf("Rollback = %v, want ErrRollbackInterference", err)
			}
			if _, err := os.Lstat(filepath.Join(dir, AppDB)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("app.db was restored beside a stale %s: %v", suffix, err)
			}
			if _, err := os.Lstat(filepath.Join(dir, RollbackIntent)); err != nil {
				t.Fatalf("the rollback intent: %v", err)
			}
		})
	}
}

// After a crash that left the restored copy at app.db, a file there that isn't the snapshot's
// copy (an older build opened it, or someone put another one there) is refused: the
// rollback can't tell what it is, so it changes nothing more and keeps its intent.
func TestRollbackRefusesAChangedRestoredFile(t *testing.T) {
	dir, fsys, from := staleWAL(t)
	if out, err := runRollbackHelper(t, dir, "rollback-restored-synced:1"); !killed(err) {
		t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
	}
	rawSet(t, dir, "INSERT INTO meta (key, value) VALUES ('after', '1')")
	before := tree(t, dir)
	_, err := rollback(context.Background(), dir, fsys, rollbackNow)
	if !errors.Is(err, ErrRollbackInterference) {
		t.Fatalf("Rollback = %v, want ErrRollbackInterference", err)
	}
	if after := tree(t, dir); fmt.Sprint(after) != fmt.Sprint(before) || after[RollbackIntent] == "" || after[snapshotName(from, from+2)] == "" {
		t.Fatalf("the refusal changed the state directory:\nbefore %v\nafter  %v", before, after)
	}
}

// assertInterference runs a rollback that must stop with ErrRollbackInterference and change
// nothing more: the state directory, rollback folder included, stays as it was.
func assertInterference(t *testing.T, dir string, fsys fs.FS, want string) {
	t.Helper()
	before := tree(t, dir)
	_, err := rollback(context.Background(), dir, fsys, rollbackNow)
	if !errors.Is(err, ErrRollbackInterference) || !strings.Contains(err.Error(), want) {
		t.Fatalf("Rollback = %v, want ErrRollbackInterference saying %q", err, want)
	}
	if after := tree(t, dir); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("the refusal changed the state directory:\nbefore %v\nafter  %v", before, after)
	}
}

// After a crash that left the restored copy in place, a WAL beside it is refused: it would
// be replayed into the restored file at the next open.
func TestRollbackRefusesAWALBesideTheRestoredFileAfterACrash(t *testing.T) {
	dir, fsys, _ := staleWAL(t)
	if out, err := runRollbackHelper(t, dir, "rollback-restored-synced:1"); !killed(err) {
		t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
	}
	writeFile(t, filepath.Join(dir, AppDB+"-wal"), []byte("stale"))
	assertInterference(t, dir, fsys, "beside the restored database")
}

// A well-formed rollback intent for another build's migration is refused before anything
// moves.
func TestRollbackRefusesARollbackIntentFromAnotherBuild(t *testing.T) {
	dir, fsys, from := failedMigration(t)
	writeFile(t, filepath.Join(dir, RollbackIntent), fmt.Appendf(nil, `{"from":%d,"to":%d,"snapshot":%q,"dir":%q}`,
		from, from+3, snapshotName(from, from+3), rollbackDirName(rollbackNow)))
	assertInterference(t, dir, fsys, "this build's last migration")
}

// migrate.intent rewritten while a rollback was interrupted (it names another migration
// now) isn't removed.
func TestRollbackRefusesAChangedMigrateIntent(t *testing.T) {
	dir, fsys, from := staleWAL(t)
	if out, err := runRollbackHelper(t, dir, "rollback-snapshot-moved:1"); !killed(err) {
		t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
	}
	writeIntentFile(t, dir, intent{From: from + 1, To: from + 2, Snapshot: snapshotName(from+1, from+2)})
	assertInterference(t, dir, fsys, "migrate.intent names")
}

// Once migrate.intent is retired, the previous build may open app.db (it doesn't know
// rollback.intent): the next run only removes its own intent, without judging app.db.
func TestRollbackFinishesAfterThePreviousBuildStarted(t *testing.T) {
	dir, fsys, from := staleWAL(t)
	if out, err := runRollbackHelper(t, dir, "rollback-retired:1"); !killed(err) {
		t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
	}
	rawSet(t, dir, "INSERT INTO meta (key, value) VALUES ('previous build', '1')")
	r, err := rollback(context.Background(), dir, fsys, rollbackNow)
	if err != nil {
		t.Fatalf("the next run: %v", err)
	}
	assertRolledBack(t, dir, r, from, from+2)
}

// A copy of the snapshot that fails its check is never put in place.
func TestRollbackRefusesACopyThatDoesntVerify(t *testing.T) {
	dir, fsys, _ := failedMigration(t)
	setKillPoint(t, func(p string) {
		if p == "rollback-copied" {
			writeFile(t, filepath.Join(dir, AppDB+".tmp"), []byte(strings.Repeat("garbage ", 1024)))
		}
	})
	if _, err := rollback(context.Background(), dir, fsys, rollbackNow); !errors.Is(err, ErrRollbackInterference) {
		t.Fatalf("Rollback = %v, want ErrRollbackInterference", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, AppDB)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a copy that doesn't verify was put in place: %v", err)
	}
}

// A file an older build left at app.db after a crash in steps 3 to 6 (a new, empty database
// where the original was moved; or the restored copy, opened and changed) is refused, with
// both intents kept. The runbook's fix, moving it aside, lets the next run finish: it copies
// the snapshot again from wherever it is.
func TestRollbackAfterAStrayAppDB(t *testing.T) {
	for _, at := range []string{"rollback-moved-synced:1", "rollback-restored-synced:1", "rollback-snapshot-moved:1"} {
		t.Run(at, func(t *testing.T) {
			dir, fsys, from := staleWAL(t)
			if out, err := runRollbackHelper(t, dir, at); !killed(err) {
				t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
			}
			rawSet(t, dir, "CREATE TABLE IF NOT EXISTS stray (x INTEGER)") // an older build's open
			assertInterference(t, dir, fsys, "neither the moved database nor the snapshot's copy")
			stray := filepath.Join(dir, "stray")
			must(t, os.Mkdir(stray, 0o700))
			for _, n := range []string{AppDB, AppDB + "-wal", AppDB + "-shm"} {
				if err := os.Rename(filepath.Join(dir, n), filepath.Join(stray, n)); err != nil && !errors.Is(err, fs.ErrNotExist) {
					t.Fatal(err)
				}
			}
			r, err := rollback(context.Background(), dir, fsys, rollbackNow)
			if err != nil {
				t.Fatalf("after moving the stray file aside: %v", err)
			}
			must(t, os.RemoveAll(stray))
			assertRolledBack(t, dir, r, from, from+2)
		})
	}
}

// A rollback intent that doesn't parse, or names a folder that isn't a rollback folder, is
// refused before anything moves.
func TestRollbackRefusesABadRollbackIntent(t *testing.T) {
	for _, body := range []string{
		"{",
		`{"from":2,"to":4,"snapshot":"pre-migrate-2-4.db","dir":"../x"}`,
		`{"from":2,"to":4,"snapshot":"../app.db","dir":"rollback-20261113T101500Z"}`,
		`{"from":2,"to":4,"snapshot":"pre-migrate-2-4.db","dir":"rollback-20261113T101500Z","x":1}`,
	} {
		t.Run(body, func(t *testing.T) {
			dir, fsys, _ := failedMigration(t)
			writeFile(t, filepath.Join(dir, RollbackIntent), []byte(body))
			before := tree(t, dir)
			if _, err := rollback(context.Background(), dir, fsys, rollbackNow); !errors.Is(err, ErrRollbackInterference) {
				t.Fatalf("Rollback = %v, want ErrRollbackInterference", err)
			}
			if after := tree(t, dir); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Fatalf("the state directory changed: %v", after)
			}
		})
	}
}
