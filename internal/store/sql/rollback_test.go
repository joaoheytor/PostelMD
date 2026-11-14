//go:build linux

package sql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"
)

// rollbackNow is the clock the rollback tests pass: its folder is rollbackDirName(rollbackNow).
var rollbackNow = time.Date(2026, 11, 13, 10, 15, 0, 0, time.UTC)

// failingMigrations are this build's migrations and two later ones, the second of which
// fails: opening a database at the build's version with them leaves ADR-0015's "between"
// row (the intent, the snapshot, the version one past from).
func failingMigrations(t *testing.T) (fstest.MapFS, int) {
	t.Helper()
	fsys, from := laterMigrations(t, 2)
	fsys[fmt.Sprintf("%04d_later_%d.sql", from+2, from+2)] = &fstest.MapFile{Data: []byte("CREATE TABLE later_x (")}
	return fsys, from
}

// failedMigration is a state directory whose migration from → from+2 failed at its second
// step, with a meta row ("before") written before it.
func failedMigration(t *testing.T) (dir string, fsys fstest.MapFS, from int) {
	t.Helper()
	dir = closedDB(t)
	rawSet(t, dir, "INSERT INTO meta (key, value) VALUES ('before', '1')")
	fsys, from = failingMigrations(t)
	if db, err := open(context.Background(), dir, discard, fsys); err == nil {
		_ = db.Close()
		t.Fatal("the failing migration succeeded")
	}
	if got := readOnDisk(t, dir, from, from+2); got != (onDisk{intent: true, snap: true, uv: 1}) {
		t.Fatalf("after the failed migration %+v", got)
	}
	return dir, fsys, from
}

// tree is every entry under dir, recursively: its type, and a regular file's SHA-256.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if !d.Type().IsRegular() {
			out[rel] = d.Type().String()
			return nil
		}
		b, err := os.ReadFile(p) //nolint:gosec // G304: the test's own temp folder
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertRolledBack checks db rollback's end state for from → to: no intent of either kind
// and no snapshot or temp in the state directory; the moved database and the snapshot in the
// rollback folder; the previous build (this build's migrations, at from) opens app.db at
// from with the row written before the migration; and this build's later migrations run
// again from there.
func assertRolledBack(t *testing.T, dir string, r RolledBack, from, to int) {
	t.Helper()
	if r.From != from || r.To != to || !r.Restored || r.Dir != rollbackDirName(rollbackNow) {
		t.Fatalf("Rollback = %+v", r)
	}
	for _, n := range []string{MigrateIntent, RollbackIntent, snapshotName(from, to), AppDB + ".tmp", AppDB + "-wal", AppDB + "-shm"} {
		if _, err := os.Lstat(filepath.Join(dir, n)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s is still in the state directory: %v", n, err)
		}
	}
	d := filepath.Join(dir, r.Dir)
	if v, err := checkCopy(context.Background(), filepath.Join(d, snapshotName(from, to))); err != nil || v != from {
		t.Fatalf("the moved snapshot: version %d, %v", v, err)
	}
	if fi, err := os.Lstat(filepath.Join(d, AppDB)); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("the moved database: %v, %v", fi, err)
	}
	if fi, err := os.Lstat(d); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the rollback folder: %v, %v", fi, err)
	}
	if fi, err := os.Lstat(filepath.Join(dir, AppDB)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the restored database: %v, %v", fi, err)
	}
	prev := embeddedFS(t) // the previous build: its last migration is from
	db, err := open(context.Background(), dir, discard, prev)
	if err != nil {
		t.Fatalf("the previous build's open: %v", err)
	}
	uv, _ := state(t, db)
	var v string
	err = db.w.QueryRow("SELECT value FROM meta WHERE key = 'before'").Scan(&v)
	_ = db.Close()
	if uv != from || err != nil || v != "1" {
		t.Fatalf("after the rollback: version %d, the row before the migration: %q, %v", uv, v, err)
	}
	again, _ := laterMigrations(t, 2)
	db, err = open(context.Background(), dir, discard, again)
	if err != nil {
		t.Fatalf("migrating again: %v", err)
	}
	defer func() { _ = db.Close() }()
	assertMigrated(t, db, dir, from, to)
}

func TestRollbackAFailedMigration(t *testing.T) {
	dir, fsys, from := failedMigration(t)
	r, err := rollback(context.Background(), dir, fsys, rollbackNow)
	if err != nil {
		t.Fatal(err)
	}
	assertRolledBack(t, dir, r, from, from+2)
}

// A committed transaction only the WAL holds (the migration's process was killed) moves with
// the database, and is never replayed into the restored file.
func TestRollbackMovesAStaleWAL(t *testing.T) {
	dir, fsys, from := staleWAL(t)
	r, err := rollback(context.Background(), dir, fsys, rollbackNow)
	if err != nil {
		t.Fatal(err)
	}
	d := filepath.Join(dir, r.Dir)
	for _, n := range []string{AppDB + "-wal", AppDB + "-shm"} {
		if _, err := os.Lstat(filepath.Join(d, n)); err != nil {
			t.Fatalf("%s didn't move with the database: %v", n, err)
		}
	}
	if !hasRow(t, filepath.Join(d, AppDB), "wal-only") {
		t.Fatal("the moved database lost the WAL's row")
	}
	if hasRow(t, filepath.Join(dir, AppDB), "wal-only") {
		t.Fatal("the WAL's row was replayed into the restored database")
	}
	assertRolledBack(t, dir, r, from, from+2)
}

// staleWAL is failedMigration's state with a committed meta row ("wal-only") that only
// app.db-wal holds: the files are copied while the connection that wrote it is still open,
// as a killed process leaves them.
func staleWAL(t *testing.T) (dir string, fsys fstest.MapFS, from int) {
	t.Helper()
	src, fsys, from := failedMigration(t)
	p, err := openPool(filepath.Join(src, AppDB), pragmas, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	for _, s := range []string{"PRAGMA wal_autocheckpoint = 0", "INSERT INTO meta (key, value) VALUES ('wal-only', '1')"} {
		if _, err := p.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	dir = filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	es, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		b, err := os.ReadFile(filepath.Join(src, e.Name())) //nolint:gosec // G304: the test's own temp folder
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, e.Name()), b)
	}
	if fi, err := os.Stat(filepath.Join(dir, AppDB+"-wal")); err != nil || fi.Size() == 0 {
		t.Fatalf("no WAL to test with: %v, %v", fi, err)
	}
	return dir, fsys, from
}

// hasRow reports whether the database at path (with its WAL, if any) has the meta row key.
func hasRow(t *testing.T, path, key string) bool {
	t.Helper()
	p, err := openPool(path, "mode=ro", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	var n int
	if err := p.QueryRow("SELECT count(*) FROM meta WHERE key = ?", key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// A migration interrupted before its snapshot was in place left the database at from: there
// is nothing to restore, so only the snapshot's temp and the intent go.
func TestRollbackBeforeTheSnapshotRetiresTheIntent(t *testing.T) {
	dir := closedDB(t)
	fsys, from := failingMigrations(t)
	if out, err := runHelper(t, dir, "-sql.crash-at=copied:1"); !killed(err) {
		t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
	}
	if got := readOnDisk(t, dir, from, from+2); got != (onDisk{intent: true, tmp: true}) {
		t.Fatalf("after the crash %+v", got)
	}
	r, err := rollback(context.Background(), dir, fsys, rollbackNow)
	if err != nil || r != (RolledBack{From: from, To: from + 2}) {
		t.Fatalf("Rollback = %+v, %v", r, err)
	}
	assertRetired(t, dir, from)
}

// assertRetired: no intent, snapshot or temp, no rollback folder, and the previous build
// opens the database at from.
func assertRetired(t *testing.T, dir string, from int) {
	t.Helper()
	snap := snapshotName(from, from+2)
	for _, n := range []string{MigrateIntent, RollbackIntent, snap, snap + ".tmp", snap + ".tmp-journal", rollbackDirName(rollbackNow)} {
		if _, err := os.Lstat(filepath.Join(dir, n)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s is in the state directory: %v", n, err)
		}
	}
	db, err := open(context.Background(), dir, discard, embeddedFS(t))
	if err != nil {
		t.Fatalf("the previous build's open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if uv, _ := state(t, db); uv != from {
		t.Fatalf("version %d, want %d", uv, from)
	}
}

func killed(err error) bool {
	var ee interface{ Sys() any }
	if !errors.As(err, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signal() == syscall.SIGKILL
}

// Every refusal changes nothing: not one file in the state directory.
func TestRollbackRefuses(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T) (dir string, fsys fstest.MapFS)
		want  string
	}{
		{"nothing to roll back", func(t *testing.T) (string, fstest.MapFS) {
			return closedDB(t), embeddedFS(t)
		}, "nothing to roll back"},
		{"the migration finished and the new version started", func(t *testing.T) (string, fstest.MapFS) {
			dir := closedDB(t)
			fsys, _ := laterMigrations(t, 2)
			openWith(t, dir, fsys)
			return dir, fsys
		}, "started after"},
		{"an intent from another build", func(t *testing.T) (string, fstest.MapFS) {
			dir, _, _ := failedMigration(t)
			fsys, _ := laterMigrations(t, 3)
			return dir, fsys
		}, "this build's last migration"},
		{"the post_migration_started file", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, from := failedMigration(t)
			writeFile(t, filepath.Join(dir, markerName(from, from+2)), nil)
			return dir, fsys
		}, "post-migration-started"},
		{"the post_migration_started row", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, from := failedMigration(t)
			rawSet(t, dir, fmt.Sprintf("INSERT INTO meta (key, value) VALUES ('%s', 'x')", markerKey(from, from+2)))
			return dir, fsys
		}, "post_migration_started row"},
		{"a damaged snapshot", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, from := failedMigration(t)
			writeFile(t, filepath.Join(dir, snapshotName(from, from+2)), []byte(strings.Repeat("not a database ", 512)))
			return dir, fsys
		}, "snapshot"},
		{"a version outside the intent", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			rawSet(t, dir, "PRAGMA user_version = 1")
			return dir, fsys
		}, "outside"},
		{"no app.db", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			must(t, os.Remove(filepath.Join(dir, AppDB)))
			return dir, fsys
		}, "database file"},
		{"the rollback folder's name is taken", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			must(t, os.Mkdir(filepath.Join(dir, rollbackDirName(rollbackNow)), 0o700))
			return dir, fsys
		}, "already there"},
		{"a symlinked intent", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			p := filepath.Join(dir, MigrateIntent)
			must(t, os.Rename(p, p+".real"))
			must(t, os.Symlink(p+".real", p))
			return dir, fsys
		}, "symlink"},
		{"an app.db.tmp", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			writeFile(t, filepath.Join(dir, AppDB+".tmp"), []byte("x"))
			return dir, fsys
		}, "app.db.tmp"},
		{"a snapshot temp beside the snapshot", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, from := failedMigration(t)
			writeFile(t, filepath.Join(dir, snapshotName(from, from+2)+".tmp"), []byte("x"))
			return dir, fsys
		}, "temp"},
		{"a WAL that is a FIFO", func(t *testing.T) (string, fstest.MapFS) {
			dir, fsys, _ := failedMigration(t)
			wal := filepath.Join(dir, AppDB+"-wal")
			if err := os.Remove(wal); err != nil && !errors.Is(err, fs.ErrNotExist) { // an empty one a read left
				t.Fatal(err)
			}
			must(t, syscall.Mkfifo(wal, 0o600))
			return dir, fsys
		}, "regular file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, fsys := c.setup(t)
			before := tree(t, dir)
			r, err := rollback(context.Background(), dir, fsys, rollbackNow)
			if !errors.Is(err, ErrRollbackRefused) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Rollback = %+v, %v; want ErrRollbackRefused saying %q", r, err, c.want)
			}
			if after := tree(t, dir); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Fatalf("the state directory changed:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
