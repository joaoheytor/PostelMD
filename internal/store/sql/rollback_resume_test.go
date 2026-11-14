//go:build linux

package sql

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
