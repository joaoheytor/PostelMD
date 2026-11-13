//go:build linux

package sql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// laterMigrations is this build's migrations plus n test-only ones: a database made with the
// build's set (version from) then has n migrations pending.
func laterMigrations(t *testing.T, n int) (fsys fstest.MapFS, from int) {
	t.Helper()
	fsys = embeddedFS(t)
	from = len(fsys)
	for i := 1; i <= n; i++ {
		v := from + i
		fsys[fmt.Sprintf("%04d_later_%d.sql", v, v)] = &fstest.MapFile{
			Data: []byte(fmt.Sprintf("CREATE TABLE later_%d (x INTEGER) STRICT;", v)),
		}
	}
	return fsys, from
}

// openWith opens a database in dir with fsys's migrations and closes it.
func openWith(t *testing.T, dir string, fsys fstest.MapFS) {
	t.Helper()
	db, err := open(context.Background(), dir, discard, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// snapshotAt writes a verified copy of a database at fsys's last version to dest.
func snapshotAt(t *testing.T, fsys fstest.MapFS, dest string) {
	t.Helper()
	dir := t.TempDir()
	db, err := open(context.Background(), dir, discard, fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Snapshot(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
}

func writeIntentFile(t *testing.T, dir string, in intent) {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, MigrateIntent), b)
}

// assertMigrated checks ADR-0015's end state of a migration from → to: the database at to
// with its records, the snapshot in place (0600, integrity_check, version from), both
// post_migration_started markers, and no intent or temp left.
func assertMigrated(t *testing.T, db *DB, dir string, from, to int) {
	t.Helper()
	if uv, recs := state(t, db); uv != to || len(recs) != to {
		t.Fatalf("user_version %d, records %v; want %d", uv, recs, to)
	}
	snap := filepath.Join(dir, snapshotName(from, to))
	if fi, err := os.Lstat(snap); err != nil || fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("the snapshot: %v, %v", fi, err)
	}
	if v, err := checkCopy(context.Background(), snap); err != nil || v != from {
		t.Fatalf("the snapshot: version %d, %v; want %d", v, err, from)
	}
	var at string
	if err := db.w.QueryRow("SELECT value FROM meta WHERE key = ?", markerKey(from, to)).Scan(&at); err != nil {
		t.Fatalf("the post_migration_started row: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(dir, markerName(from, to))); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the post_migration_started file: %v, %v", fi, err)
	}
	for _, p := range []string{MigrateIntent, MigrateIntent + ".tmp", snapshotName(from, to) + ".tmp"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s is left: %v", p, err)
		}
	}
}

// onlyDatabaseFiles fails if dir holds anything but app.db and its -wal and -shm.
func onlyDatabaseFiles(t *testing.T, dir string) {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if n := e.Name(); n != AppDB && n != AppDB+"-wal" && n != AppDB+"-shm" {
			t.Fatalf("a refused open left %s", n)
		}
	}
}

func TestMigrateExistingDatabase(t *testing.T) {
	dir := closedDB(t)
	fsys, from := laterMigrations(t, 2)
	var logs bytes.Buffer
	db, err := open(context.Background(), dir, slog.New(slog.NewTextHandler(&logs, nil)), fsys)
	if err != nil {
		t.Fatal(err)
	}
	assertMigrated(t, db, dir, from, from+2)
	line := logs.String()
	if !strings.Contains(line, "snapshot="+snapshotName(from, from+2)) || strings.Contains(line, dir) {
		t.Fatalf("the migration's log record names the snapshot by its file name only:\n%s", line)
	}
	// A second open has nothing to do: no new snapshot, the markers unchanged.
	var before string
	_ = db.w.QueryRow("SELECT value FROM meta WHERE key = ?", markerKey(from, from+2)).Scan(&before)
	if db, err = reopen(t, db, dir, fsys); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	assertMigrated(t, db, dir, from, from+2)
	var after string
	_ = db.w.QueryRow("SELECT value FROM meta WHERE key = ?", markerKey(from, from+2)).Scan(&after)
	if after != before {
		t.Fatalf("a second open rewrote the marker: %q, was %q", after, before)
	}
}

// A migration that fails (here, its SQL) leaves the intent, the snapshot and the versions it
// committed; the next start with a build whose migration works resumes from there (ADR-0015's
// "between" row), and database-only rollback stays possible until then (no marker).
func TestFailedMigrationResumes(t *testing.T) {
	dir := closedDB(t)
	fsys, from := laterMigrations(t, 2)
	broken := maps.Clone(fsys)
	last := fmt.Sprintf("%04d_later_%d.sql", from+2, from+2)
	broken[last] = &fstest.MapFile{Data: []byte("CREATE TABLE oops (x NOPE) STRICT;")}
	if db, err := open(context.Background(), dir, discard, broken); err == nil {
		_ = db.Close()
		t.Fatal("a migration with bad SQL succeeded")
	}
	if got := committedVersion(t, dir); got != from+1 {
		t.Fatalf("after the failed migration user_version is %d, want %d", got, from+1)
	}
	for _, p := range []string{MigrateIntent, snapshotName(from, from+2)} {
		if _, err := os.Lstat(filepath.Join(dir, p)); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, markerName(from, from+2))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed migration wrote the post_migration_started file: %v", err)
	}
	db, err := open(context.Background(), dir, discard, fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	assertMigrated(t, db, dir, from, from+2)
}
