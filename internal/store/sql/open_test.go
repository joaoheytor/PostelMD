//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// newDB opens app.db in a fresh state directory (0700, as layout makes it) and closes it at
// the end of the test.
func newDB(t *testing.T) (*DB, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), dir, discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, dir
}

// pragma reads one pragma's value on a single connection of p.
func pragma(t *testing.T, p *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := p.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

func TestPragmasOnEveryConnection(t *testing.T) {
	db, _ := newDB(t)
	snap, err := openPool(db.path, snapshotQuery, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snap.Close() })
	want := map[string]string{
		"journal_mode": "wal", "synchronous": "2", "busy_timeout": "5000", "foreign_keys": "1",
		"trusted_schema": "0", "cell_size_check": "1", "journal_size_limit": "67108864",
	}
	for _, c := range []struct {
		name      string
		pool      *sql.DB
		queryOnly string
	}{{"writer", db.w, "0"}, {"reader", db.r, "1"}, {"snapshot", snap, "0"}} {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range want {
				if got := pragma(t, c.pool, k); got != v {
					t.Errorf("%s = %s, want %s", k, got, v)
				}
			}
			if got := pragma(t, c.pool, "query_only"); got != c.queryOnly {
				t.Errorf("query_only = %s, want %s", got, c.queryOnly)
			}
		})
	}
}

// Defensive mode (SQLITE_DBCONFIG_DEFENSIVE) stops writable_schema from editing the schema.
func TestDefensiveRefusesSchemaWrites(t *testing.T) {
	db, _ := newDB(t)
	_, _ = db.w.Exec("PRAGMA writable_schema = ON")
	_, err := db.w.Exec("UPDATE sqlite_schema SET sql = 'x' WHERE name = 'meta'")
	if err == nil {
		t.Fatal("the schema table was writable: defensive mode is off")
	}
}

func TestReadConnectionsCannotWrite(t *testing.T) {
	db, _ := newDB(t)
	snap, err := openPool(db.path, snapshotQuery, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snap.Close() })
	for _, c := range []struct {
		name string
		pool *sql.DB
	}{{"reader", db.r}, {"snapshot", snap}} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.pool.Exec("INSERT INTO meta (key, value) VALUES ('k', 'v')"); err == nil {
				t.Fatal("a read-only connection wrote a row")
			}
			if _, err := c.pool.Exec("PRAGMA user_version = 42"); err == nil && pragma(t, db.w, "user_version") == "42" {
				t.Fatal("a read-only connection changed user_version")
			}
		})
	}
}

func TestFileModes(t *testing.T) {
	old := syscall.Umask(0) // process-wide (no t.Parallel here): only our mode limits the files
	t.Cleanup(func() { syscall.Umask(old) })
	db, dir := newDB(t)
	if _, err := db.w.Exec("INSERT INTO meta (key, value) VALUES ('k', 'v')"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{AppDB, AppDB + "-wal", AppDB + "-shm"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if m := fi.Mode().Perm(); m != 0o600 {
			t.Errorf("%s mode %04o, want 0600", name, m)
		}
	}
}

// A state directory whose path holds URI syntax still opens the right file, with the query
// applied (a raw "?" would end the path, and the read pool would open read-write).
func TestOpenEscapesThePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a?b #c%41")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), dir, discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := os.Stat(filepath.Join(dir, AppDB+"-wal")); err != nil {
		t.Fatalf("the database isn't in the directory: %v", err)
	}
	if got := pragma(t, db.r, "query_only"); got != "1" {
		t.Fatalf("the reader's query wasn't applied: query_only = %s", got)
	}
}

func TestOpenRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "elsewhere.db"), filepath.Join(dir, AppDB)); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), dir, discard); err == nil {
		t.Fatal("opened a database through a symlink")
	}
	if _, err := os.Lstat(filepath.Join(dir, "elsewhere.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the symlink's target was created: %v", err)
	}
}

func TestLooseModesAreWarned(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o770); err != nil { //nolint:gosec // G302: the loose mode under test
		t.Fatal(err)
	}
	var buf strings.Builder
	db, err := Open(context.Background(), dir, slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "state directory") {
		t.Fatalf("no warning for a 0770 state directory:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), dir) {
		t.Fatalf("the warning quotes the path:\n%s", buf.String())
	}
}

// Every write transaction is BEGIN IMMEDIATE: it holds the write lock from its start, so
// another connection can't begin writing meanwhile.
func TestWriteTransactionsBeginImmediate(t *testing.T) {
	db, _ := newDB(t)
	other, err := sql.Open("sqlite", fileURI(db.path, "_pragma=busy_timeout(0)"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	err = db.writeTx(context.Background(), func(*sql.Tx) error {
		_, err := other.Exec("BEGIN IMMEDIATE")
		if err == nil {
			_, _ = other.Exec("ROLLBACK")
			return errors.New("another connection began writing inside our transaction")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The writer connection is taken with a deadline, never waited for without bound.
func TestWriterWaitHasADeadline(t *testing.T) {
	db, _ := newDB(t)
	old := writerWait
	writerWait = 50 * time.Millisecond
	t.Cleanup(func() { writerWait = old })
	err := db.writeTx(context.Background(), func(*sql.Tx) error {
		return db.writeTx(context.Background(), func(*sql.Tx) error { return nil })
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a second writer got %v, want a deadline error", err)
	}
}

func TestQuickCheckRefusesADamagedDatabase(t *testing.T) {
	db, dir := newDB(t)
	if _, err := db.w.Exec("CREATE TABLE filler (b BLOB); WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 200) " +
		"INSERT INTO filler SELECT randomblob(2000) FROM n"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, AppDB)
	b, err := os.ReadFile(path) //nolint:gosec // G304: the test's own file
	if err != nil {
		t.Fatal(err)
	}
	for i := 4096 * 3; i < 4096*6; i++ { // pages 4-6: b-tree pages of filler, not the header
		b[i] = 0xA5
	}
	if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // G703: the test's own temp file
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), dir, discard); !errors.Is(err, ErrDamaged) {
		t.Fatalf("Open on a damaged file: %v, want ErrDamaged", err)
	}
}
