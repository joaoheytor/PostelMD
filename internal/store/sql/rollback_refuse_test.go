//go:build linux

package sql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
)

// Every refusal changes nothing in the state directory. With a WAL beside app.db, the read
// goes through it, and SQLite may rewrite (or create) its -shm index: that file, and only
// that one, is left out of the comparison (ADR-0035).
func TestRollbackRefuses(t *testing.T) {
	notADatabase := []byte(strings.Repeat("not a database ", 512))
	for _, c := range []struct {
		name  string
		setup func(t *testing.T) (dir string, fsys fstest.MapFS)
		want  string
		wal   bool // a non-empty WAL is beside app.db
	}{
		{"nothing to roll back", func(t *testing.T) (string, fstest.MapFS) {
			return closedDB(t), embeddedFS(t)
		}, "nothing to roll back", false},
		{"the migration finished and the new version started", func(t *testing.T) (string, fstest.MapFS) {
			dir := closedDB(t)
			fsys, _ := laterMigrations(t, 2)
			openWith(t, dir, fsys)
			return dir, fsys
		}, "started after", false},
		{"an intent from another build", func(t *testing.T) (string, fstest.MapFS) {
			dir, _, _ := failedMigration(t)
			fsys, _ := laterMigrations(t, 3)
			return dir, fsys
		}, "this build's last migration", false},
		{"the post_migration_started file", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, from := failedMigration(t)
			writeFile(t, filepath.Join(dir, markerName(from, from+2)), nil)
			return dir, fsys
		}, "post-migration-started", false},
		{"the post_migration_started row", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, from := failedMigration(t)
			rawSet(t, dir, fmt.Sprintf("INSERT INTO meta (key, value) VALUES ('%s', 'x')", markerKey(from, from+2)))
			return dir, fsys
		}, "post_migration_started row", false},
		{"the post_migration_started row, committed only in the WAL", func(t *testing.T) (string, fstest.MapFS) {
			// A migration killed right after writing its row: the row is in the WAL, the
			// file isn't written yet. A read that skipped the WAL would roll it back.
			dir := closedDB(t)
			fsys, _ := laterMigrations(t, 2)
			if out, err := runHelper(t, dir, "-sql.crash-at=marked-db:1"); !killed(err) {
				t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
			}
			if fi, err := os.Stat(filepath.Join(dir, AppDB+"-wal")); err != nil || fi.Size() == 0 {
				t.Fatalf("no WAL holds the row: %v, %v", fi, err)
			}
			return dir, fsys
		}, "post_migration_started row", true},
		{"a damaged snapshot", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, from := failedMigration(t)
			writeFile(t, filepath.Join(dir, snapshotName(from, from+2)), notADatabase)
			return dir, fsys
		}, "the pre-migration snapshot doesn't verify: fix forward", false},
		{"a damaged snapshot, with a WAL", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, from := staleWAL(t)
			writeFile(t, filepath.Join(dir, snapshotName(from, from+2)), notADatabase)
			return dir, fsys
		}, "the pre-migration snapshot doesn't verify", true},
		{"a damaged snapshot, with a WAL and no -shm", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, from := staleWAL(t)
			writeFile(t, filepath.Join(dir, snapshotName(from, from+2)), notADatabase)
			must(t, os.Remove(filepath.Join(dir, AppDB+"-shm")))
			return dir, fsys
		}, "", true},
		{"no snapshot, and app.db past from", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, from := failedMigration(t)
			must(t, os.Remove(filepath.Join(dir, snapshotName(from, from+2))))
			return dir, fsys
		}, "snapshot is missing", false},
		{"a version outside the intent", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			rawSet(t, dir, "PRAGMA user_version = 1")
			return dir, fsys
		}, "outside", false},
		{"no app.db", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			must(t, os.Remove(filepath.Join(dir, AppDB)))
			return dir, fsys
		}, "database file", false},
		{"an app.db-journal", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			writeFile(t, filepath.Join(dir, AppDB+"-journal"), []byte("x"))
			return dir, fsys
		}, "app.db-journal", false},
		{"the rollback folder's name is taken", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			must(t, os.Mkdir(filepath.Join(dir, rollbackDirName(rollbackNow)), 0o700))
			return dir, fsys
		}, "already there", false},
		{"a symlinked intent", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			p := filepath.Join(dir, MigrateIntent)
			must(t, os.Rename(p, p+".real"))
			must(t, os.Symlink(p+".real", p))
			return dir, fsys
		}, "symlink", false},
		{"an app.db.tmp", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			writeFile(t, filepath.Join(dir, AppDB+".tmp"), []byte("x"))
			return dir, fsys
		}, "app.db.tmp", false},
		{"a snapshot temp beside the snapshot", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, from := failedMigration(t)
			writeFile(t, filepath.Join(dir, snapshotName(from, from+2)+".tmp"), []byte("x"))
			return dir, fsys
		}, "temp", false},
		{"a WAL that is a FIFO", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			must(t, syscall.Mkfifo(filepath.Join(dir, AppDB+"-wal"), 0o600))
			return dir, fsys
		}, "regular file", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, fsys := c.setup(t)
			before := tree(t, dir)
			r, err := rollback(context.Background(), dir, fsys, rollbackNow)
			if !errors.Is(err, ErrRollbackRefused) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Rollback = %+v, %v; want ErrRollbackRefused saying %q", r, err, c.want)
			}
			if strings.Contains(err.Error(), "damaged-<date>") { // ErrDamaged's fix is for a start, not here
				t.Fatalf("the refusal names the damaged-database fix: %v", err)
			}
			after := tree(t, dir)
			if c.wal {
				delete(before, AppDB+"-shm")
				delete(after, AppDB+"-shm")
			}
			if fmt.Sprint(after) != fmt.Sprint(before) {
				t.Fatalf("the state directory changed:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}
