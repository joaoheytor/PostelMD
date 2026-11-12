//go:build linux

package sql

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// No connection can ATTACH: through it a read-only connection could open app.db, or a new
// file, read-write. VACUUM INTO from a connection other than Snapshot's fails too.
func TestConnectionsDenyAttach(t *testing.T) {
	db, dir := newDB(t)
	snap, err := openPool(db.path, snapshotQuery, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snap.Close() })
	for _, c := range []struct {
		name string
		pool *sql.DB
	}{{"writer", db.w}, {"reader", db.r}, {"snapshot", snap}} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.pool.Exec("ATTACH ? AS self", db.path); err == nil {
				_, ierr := c.pool.Exec("INSERT INTO self.meta (key, value) VALUES ('attached', 'v')")
				t.Fatalf("ATTACH of app.db succeeded (then INSERT: %v)", ierr)
			}
			fresh := filepath.Join(dir, c.name+"-new.db")
			if _, err := c.pool.Exec("ATTACH ? AS fresh", fresh); err == nil {
				t.Fatal("ATTACH of a new path succeeded")
			}
			if _, err := os.Lstat(fresh); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ATTACH created a file: %v", err)
			}
			out := filepath.Join(dir, c.name+"-vacuum.db")
			if _, err := c.pool.Exec("VACUUM INTO ?", out); err == nil {
				t.Fatal("VACUUM INTO succeeded outside Snapshot")
			}
			if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("VACUUM INTO created a file: %v", err)
			}
		})
	}
	var n int
	if err := db.w.QueryRow("SELECT count(*) FROM meta WHERE key = 'attached'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("a row was written through ATTACH: %d, %v", n, err)
	}
}
