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

// TestRebuildTakesOverAMigrationRollbackCantUndo: an interrupted migration that db rollback
// can't undo, for one of the reasons ADR-0040 lists (app.db absent or damaged, the new
// version started, the snapshot doesn't verify), joins the rebuild's original move: its
// intent, snapshot and post-migration-started files go into the damaged folder, and the
// new database opens.
func TestRebuildTakesOverAMigrationRollbackCantUndo(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string, from int)
	}{
		{"app.db absent", func(t *testing.T, dir string, _ int) { mustRemove(t, filepath.Join(dir, AppDB)) }},
		{"app.db damaged", func(t *testing.T, dir string, _ int) {
			mustRemove(t, filepath.Join(dir, AppDB))
			writeFile(t, filepath.Join(dir, AppDB), []byte(strings.Repeat("not a database ", 16)))
		}},
		{"a post-migration-started file", func(t *testing.T, dir string, from int) {
			writeFile(t, filepath.Join(dir, markerName(from, from+2)), nil)
		}},
		{"a post-migration-started row", func(t *testing.T, dir string, from int) {
			rawSet(t, dir, "INSERT INTO meta (key, value) VALUES ('"+markerKey(from, from+2)+"', '1')")
		}},
		{"the snapshot has another version", func(t *testing.T, dir string, from int) {
			b, err := os.ReadFile(filepath.Join(dir, AppDB)) //nolint:gosec // G304: the test's own state folder
			must(t, err)
			mustRemove(t, filepath.Join(dir, snapshotName(from, from+2)))
			writeFile(t, filepath.Join(dir, snapshotName(from, from+2)), b) // app.db is past from
		}},
		{"the snapshot doesn't verify", func(t *testing.T, dir string, from int) {
			mustRemove(t, filepath.Join(dir, snapshotName(from, from+2)))
			writeFile(t, filepath.Join(dir, snapshotName(from, from+2)), []byte(strings.Repeat("x", 4096)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, fsys, from := failedMigration(t)
			tc.setup(t, dir, from)
			r, err := beginRebuild(ctx, dir, rebuildNow, fsys)
			must(t, err)
			if !r.Migration {
				t.Fatalf("the intent doesn't record the migration's files: %+v", r)
			}
			if _, err := MoveAsideForRebuild(ctx, dir, r); err != nil {
				t.Fatal(err)
			}
			for _, n := range []string{MigrateIntent, snapshotName(from, from+2)} {
				if exists(dir, n) || !exists(filepath.Join(dir, r.DamagedDir()), n) {
					t.Fatalf("%s didn't move into the damaged folder", n)
				}
			}
			db, err := OpenForRebuild(ctx, dir, discard)
			must(t, err)
			must(t, db.Close())
		})
	}
}

// TestRebuildRefusesAMigrationForOtherReasons: any other reason db rollback refuses (another
// build's migration, a stray app.db.tmp, the rollback folder already there, an I/O or
// permission error) refuses the rebuild too, with rollback's own message, and nothing written.
func TestRebuildRefusesAMigrationForOtherReasons(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, want string
		setup      func(t *testing.T, dir string)
		other      bool // run with another build's migrations
	}{
		{name: "another build's migration", want: "run db rollback with the build that started that migration", other: true,
			setup: func(t *testing.T, dir string) { mustRemove(t, filepath.Join(dir, AppDB)) }},
		{name: "a stray app.db.tmp", want: "app.db.tmp",
			setup: func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, AppDB+".tmp"), nil) }},
		{name: "the rollback folder already there", want: "already there",
			setup: func(t *testing.T, dir string) { mustMkdir(t, filepath.Join(dir, rollbackDirName(rebuildNow))) }},
		{name: "the snapshot unreadable", want: "snapshot doesn't verify",
			setup: func(t *testing.T, dir string) {
				if os.Geteuid() == 0 {
					t.Skip("root reads a file without read permission")
				}
				es, err := os.ReadDir(dir)
				must(t, err)
				for _, e := range es {
					if strings.HasPrefix(e.Name(), "pre-migrate-") && strings.HasSuffix(e.Name(), ".db") {
						must(t, os.Chmod(filepath.Join(dir, e.Name()), 0))
					}
				}
			}},
		{name: "migrate.intent unreadable", want: "permission denied",
			setup: func(t *testing.T, dir string) {
				if os.Geteuid() == 0 {
					t.Skip("root reads a file without read permission")
				}
				must(t, os.Chmod(filepath.Join(dir, MigrateIntent), 0))
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, fsys, _ := failedMigration(t)
			tc.setup(t, dir)
			if tc.other {
				fsys, _ = laterMigrations(t, 3)
			}
			_, err := beginRebuild(ctx, dir, rebuildNow, fsys)
			if !errors.Is(err, ErrRebuildRefused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("beginRebuild = %v, want a refusal with %q", err, tc.want)
			}
			if exists(dir, RebuildIntent) {
				t.Fatal("an intent was written")
			}
		})
	}
}
