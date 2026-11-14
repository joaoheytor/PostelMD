//go:build linux

package sql

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedMigrationsLoad(t *testing.T) {
	ms, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 || ms[0].name != "0001_schema_migrations_and_meta.sql" {
		t.Fatalf("embedded migrations: %+v", ms)
	}
}

func TestLoadMigrationsRefusesBadSets(t *testing.T) {
	ok := &fstest.MapFile{Data: []byte("SELECT 1;")}
	for _, c := range []struct {
		name string
		fsys fstest.MapFS
	}{
		{"gap", fstest.MapFS{"0001_a.sql": ok, "0003_c.sql": ok}},
		{"not from 1", fstest.MapFS{"0002_b.sql": ok}},
		{"bad name", fstest.MapFS{"0001_a.sql": ok, "0002-B.sql": ok}},
		{"duplicate number", fstest.MapFS{"0001_a.sql": ok, "0001_b.sql": ok}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := loadMigrations(c.fsys); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// reopen closes db and opens the same state directory again with fsys's migrations.
func reopen(t *testing.T, db *DB, dir string, fsys fstest.MapFS) (*DB, error) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return open(context.Background(), dir, discard, fsys)
}

func embeddedFS(t *testing.T) fstest.MapFS {
	t.Helper()
	ms, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	m := fstest.MapFS{}
	for _, x := range ms {
		m[x.name] = &fstest.MapFile{Data: []byte(x.sql)}
	}
	return m
}

// state reads user_version and the recorded migrations.
func state(t *testing.T, db *DB) (int, []string) {
	t.Helper()
	var uv int
	if err := db.w.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatal(err)
	}
	rows, err := db.w.Query("SELECT version || ' ' || name || ' ' || sha256 FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var recs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, s)
	}
	return uv, recs
}

func TestMigrationsAreIdempotent(t *testing.T) {
	db, dir := newDB(t)
	uv, recs := state(t, db)
	ms, _ := loadMigrations(migrationFiles)
	if uv != len(ms) || len(recs) != len(ms) {
		t.Fatalf("after the first open: user_version %d, %d records, want %d", uv, len(recs), len(ms))
	}
	for range 2 {
		var err error
		if db, err = reopen(t, db, dir, embeddedFS(t)); err != nil {
			t.Fatal(err)
		}
		uv2, recs2 := state(t, db)
		if uv2 != uv || strings.Join(recs2, "\n") != strings.Join(recs, "\n") {
			t.Fatalf("a second open changed the migrations: %d %v, was %d %v", uv2, recs2, uv, recs)
		}
	}
	_ = db.Close()
}

// rawSet changes the database outside the runner (as a newer or tampered build would).
func rawSet(t *testing.T, dir, stmt string) {
	t.Helper()
	p, err := openPool(filepath.Join(dir, AppDB), pragmas, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if _, err := p.Exec(stmt); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRefuses(t *testing.T) {
	two, _ := laterMigrations(t, 1) // one migration pending
	for _, c := range []struct {
		name   string
		tamper string // run on the closed database before the second open
		fsys   fstest.MapFS
		want   error
	}{
		{"a newer database", "PRAGMA user_version = 99", embeddedFS(t), ErrNewerDatabase},
		{"an edited migration", "UPDATE schema_migrations SET sha256 = 'x' WHERE version = 1", embeddedFS(t), ErrEditedMigration},
		{"a missing record", "DELETE FROM schema_migrations", embeddedFS(t), ErrEditedMigration},
		// The refusals come before any migration: with migrations pending, nothing is written
		// (no intent, no snapshot).
		{"a newer database, with migrations this build would run", "PRAGMA user_version = 99", two, ErrNewerDatabase},
		{"an edited migration, with migrations pending", "UPDATE schema_migrations SET sha256 = 'x' WHERE version = 1", two, ErrEditedMigration},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, dir := newDB(t)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if c.tamper != "" {
				rawSet(t, dir, c.tamper)
			}
			before, err := os.ReadFile(filepath.Join(dir, AppDB)) //nolint:gosec // G304: the test's own file
			if err != nil {
				t.Fatal(err)
			}
			if db2, err := open(context.Background(), dir, discard, c.fsys); !errors.Is(err, c.want) {
				if db2 != nil {
					_ = db2.Close()
				}
				t.Fatalf("got %v, want %v", err, c.want)
			}
			after, err := os.ReadFile(filepath.Join(dir, AppDB)) //nolint:gosec // G304: the test's own file
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("a refused open changed the database file")
			}
			if wal, err := os.Stat(filepath.Join(dir, AppDB+"-wal")); err == nil && wal.Size() != 0 {
				t.Fatalf("a refused open left %d bytes in the WAL", wal.Size())
			}
			onlyDatabaseFiles(t, dir)
		})
	}
}
