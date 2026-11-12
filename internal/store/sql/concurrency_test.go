//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// The read pool reads committed snapshots while the writer commits, without errors (WAL:
// readers never block the writer). Run under -race (make test).
func TestReadersAndWriterConcurrently(t *testing.T) {
	db, _ := newDB(t)
	const writes = 200
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, readPoolSize+1)
	done := make(chan struct{})
	wg.Go(func() {
		defer close(done)
		for i := range writes {
			err := db.writeTx(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES (?, 'v')", strconv.Itoa(i))
				return err
			})
			if err != nil {
				errs <- err
				return
			}
		}
	})
	for range readPoolSize {
		wg.Go(func() {
			last := 0
			for {
				var n int
				if err := db.r.QueryRowContext(ctx, "SELECT count(*) FROM meta").Scan(&n); err != nil {
					errs <- err
					return
				}
				if n < last {
					errs <- errors.New("a reader saw the row count go down")
					return
				}
				last = n
				select {
				case <-done:
					return
				default:
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var n int
	if err := db.r.QueryRow("SELECT count(*) FROM meta").Scan(&n); err != nil || n != writes {
		t.Fatalf("after the writes: %d rows, %v", n, err)
	}
}

func TestSnapshot(t *testing.T) {
	db, dir := newDB(t)
	if _, err := db.w.Exec("INSERT INTO meta (key, value) VALUES ('k', 'v')"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "copy.db")
	v, err := db.Snapshot(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	if ms, _ := loadMigrations(migrationFiles); v != len(ms) {
		t.Fatalf("copy's user_version %d, want %d", v, len(ms))
	}
	fi, err := os.Stat(dest)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("copy: %v, %v", fi, err)
	}
	c, err := openPool(dest, "mode=ro", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	var got string
	if err := c.QueryRow("SELECT value FROM meta WHERE key = 'k'").Scan(&got); err != nil || got != "v" {
		t.Fatalf("the copy's row: %q, %v", got, err)
	}
	// An existing destination is refused and left alone.
	if _, err := db.Snapshot(context.Background(), dest); !errors.Is(err, os.ErrExist) {
		t.Fatalf("a second snapshot onto the copy: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("the refused snapshot removed the existing copy: %v", err)
	}
	// A failed copy leaves nothing behind.
	failed := filepath.Join(dir, "failed.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.Snapshot(ctx, failed); err == nil {
		t.Fatal("a snapshot with a canceled context succeeded")
	}
	if _, err := os.Lstat(failed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed snapshot left its file: %v", err)
	}
}
