//go:build linux

package sql

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// closedDB makes a migrated app.db in a fresh state directory and closes it.
func closedDB(t *testing.T) string {
	t.Helper()
	db, dir := newDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// setKillPoint replaces the test hook for one test.
func setKillPoint(t *testing.T, fn func(string)) {
	t.Helper()
	old := killPoint
	killPoint = fn
	t.Cleanup(func() { killPoint = old })
}

// ErrDamaged, which points the operator to `db rebuild`, comes only from a damaged file or
// one that isn't a database; every other failure is a plain error.
func TestOpenErrorKinds(t *testing.T) {
	for _, c := range []struct {
		name    string
		damaged bool
		setup   func(t *testing.T, dir string) context.Context
	}{
		{"not a database", true, func(t *testing.T, dir string) context.Context {
			writeFile(t, filepath.Join(dir, AppDB), bytes.Repeat([]byte("not a database\n"), 300))
			return context.Background()
		}},
		{"canceled context", false, func(*testing.T, string) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
		{"busy", false, func(t *testing.T, dir string) context.Context {
			holdExclusive(t, filepath.Join(dir, AppDB))
			return context.Background()
		}},
		{"permission", false, func(t *testing.T, dir string) context.Context {
			if os.Geteuid() == 0 {
				t.Skip("root reads a 0000 file")
			}
			if err := os.Chmod(filepath.Join(dir, AppDB), 0); err != nil {
				t.Fatal(err)
			}
			return context.Background()
		}},
		{"symlinked wal", false, func(t *testing.T, dir string) context.Context {
			if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, AppDB+"-wal")); err != nil {
				t.Fatal(err)
			}
			return context.Background()
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := closedDB(t)
			ctx := c.setup(t, dir)
			db, err := Open(ctx, dir, discard)
			if err == nil {
				_ = db.Close()
				t.Fatal("opened")
			}
			if errors.Is(err, ErrDamaged) != c.damaged {
				t.Fatalf("got %v; damaged: want %v", err, c.damaged)
			}
			if _, err := os.Lstat(filepath.Join(dir, "elsewhere")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a symlink's target was created: %v", err)
			}
		})
	}
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // G703: the test's own temp file
		t.Fatal(err)
	}
}

// holdExclusive keeps an exclusive lock on the database at path until the test ends, as
// another process would.
func holdExclusive(t *testing.T, path string) {
	t.Helper()
	p, err := sql.Open("sqlite", fileURI(path, "_pragma=locking_mode(EXCLUSIVE)"))
	if err != nil {
		t.Fatal(err)
	}
	p.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = p.Close() })
	if _, err := p.Exec("INSERT INTO meta (key, value) VALUES ('lock', 'v')"); err != nil {
		t.Fatal(err)
	}
}

// A file with tables but user_version 0 was made by another program: refused, unchanged.
func TestOpenRefusesAForeignDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, AppDB)
	p, err := sql.Open("sqlite", fileURI(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec("CREATE TABLE theirs (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	_ = p.Close()
	before, _ := os.ReadFile(path) //nolint:gosec // G304: the test's own file
	if db, err := Open(context.Background(), dir, discard); !errors.Is(err, ErrForeignDatabase) {
		if db != nil {
			_ = db.Close()
		}
		t.Fatalf("got %v, want ErrForeignDatabase", err)
	}
	after, _ := os.ReadFile(path) //nolint:gosec // G304: the test's own file
	if !bytes.Equal(before, after) {
		t.Fatal("the refused open changed the file")
	}
}

// fixSidecars, which runs before any connection: a stale -wal or -shm with group or world
// bits becomes 0600 (SQLite reuses an existing one as it is, when no connection closes it
// first); a symlink or a FIFO is refused, never followed or blocked on. (Through Open, SQLite
// itself often removes or refuses these, which hides whether fixSidecars ran.)
func TestFixSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm"} {
		for _, c := range []struct {
			name string
			make func(t *testing.T, p string)
			ok   bool
		}{
			{"stale 0644", func(t *testing.T, p string) {
				writeFile(t, p, nil)
				if err := os.Chmod(p, 0o644); err != nil { //nolint:gosec // G302: the loose mode under test
					t.Fatal(err)
				}
			}, true},
			{"symlink", func(t *testing.T, p string) {
				if err := os.Symlink(p+".target", p); err != nil {
					t.Fatal(err)
				}
			}, false},
			{"fifo", func(t *testing.T, p string) {
				if err := syscall.Mkfifo(p, 0o600); err != nil {
					t.Fatal(err)
				}
			}, false},
		} {
			t.Run(suffix+" "+c.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), AppDB)
				c.make(t, path+suffix)
				err := fixSidecars(path)
				if (err == nil) != c.ok {
					t.Fatalf("fixSidecars: %v", err)
				}
				if !c.ok {
					return
				}
				if fi, err := os.Stat(path + suffix); err != nil || fi.Mode().Perm() != 0o600 {
					t.Fatalf("after: %v, %v", fi, err)
				}
			})
		}
	}
}

// quick_check's own errors other than corruption (here, a canceled context) aren't
// ErrDamaged.
func TestQuickCheckPlainErrors(t *testing.T) {
	db, _ := newDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := quickCheck(ctx, db.w); err == nil || errors.Is(err, ErrDamaged) {
		t.Fatalf("quick_check with a canceled context: %v", err)
	}
}

// A database file swapped between the checks and SQLite's open is refused.
func TestOpenRefusesAFileSwappedWhileOpening(t *testing.T) {
	dir := closedDB(t)
	other := filepath.Join(dir, "other.db")
	writeFile(t, other, nil)
	setKillPoint(t, func(p string) {
		if p == "checked" {
			if err := os.Rename(other, filepath.Join(dir, AppDB)); err != nil {
				t.Error(err)
			}
		}
	})
	if db, err := Open(context.Background(), dir, discard); !errors.Is(err, ErrReplaced) {
		if db != nil {
			_ = db.Close()
		}
		t.Fatalf("got %v, want ErrReplaced", err)
	}
}

// A copy whose path is replaced during VACUUM INTO is refused, and the replacement, not
// Snapshot's file, is left alone.
func TestSnapshotRefusesAReplacedCopy(t *testing.T) {
	db, dir := newDB(t)
	dest := filepath.Join(dir, "copy.db")
	planted := []byte("someone else's file")
	setKillPoint(t, func(p string) {
		if p == "copied" {
			if err := os.Remove(dest); err != nil {
				t.Error(err)
			}
			writeFile(t, dest, planted)
		}
	})
	if _, err := db.Snapshot(context.Background(), dest); !errors.Is(err, ErrCopyReplaced) {
		t.Fatalf("got %v, want ErrCopyReplaced", err)
	}
	if b, err := os.ReadFile(dest); err != nil || !bytes.Equal(b, planted) { //nolint:gosec // G304: the test's own file
		t.Fatalf("the replacement was touched: %q, %v", b, err)
	}
}

// Close reports a WAL checkpoint that a reader blocked, instead of ignoring it.
func TestCloseReportsABusyCheckpoint(t *testing.T) {
	db, _ := newDB(t)
	if _, err := db.w.Exec("INSERT INTO meta (key, value) VALUES ('k', 'v')"); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", fileURI(db.path, "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM meta").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err := db.w.Exec("PRAGMA busy_timeout = 0"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); !errors.Is(err, ErrCheckpointBusy) {
		t.Fatalf("Close: %v, want ErrCheckpointBusy", err)
	}
}
