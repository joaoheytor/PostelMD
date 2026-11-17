//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// SQLite's result codes, as the fault check sees them: the primary codes that are faults,
// SQLITE_IOERR_WRITE (an extended IOERR), and codes that aren't (BUSY, BUSY_SNAPSHOT,
// CONSTRAINT, READONLY, INTERRUPT).
func TestFaultCodes(t *testing.T) {
	for code, want := range map[int]bool{10: true, 11: true, 13: true, 778: true, 5: false, 517: false, 19: false, 8: false, 9: false} {
		if got := isFaultCode(code); got != want {
			t.Errorf("isFaultCode(%d) = %v, want %v", code, got, want)
		}
	}
}

// bulk is a pass of n new rows: enough to outgrow a small max_page_count.
func bulk(n int, prefix string) [][]Change {
	units := make([][]Change, 0, n)
	for i := range n {
		units = append(units, []Change{InsertFile{Path: fmt.Sprintf("%s/%04d-%s.md", prefix, i, strings.Repeat("x", 200)),
			SHA256: sum(byte(i)), Stat: FileStat{Size: 1, Inode: uint64(1000 + i)}}}) //nolint:gosec // G115: a small count
	}
	return units
}

// capPages lets the writer's file grow by at most two pages: a large write then fails with
// SQLITE_FULL, in this process (max_page_count is per connection, and the writer has one).
func capPages(t *testing.T, db *DB) {
	t.Helper()
	var pages int
	if err := db.w.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := db.w.Exec(fmt.Sprintf("PRAGMA max_page_count = %d", pages+2)); err != nil {
		t.Fatal(err)
	}
}

// SQLITE_FULL on a write is a writer fault, in process.
func TestSQLiteFullIsAWriterFault(t *testing.T) {
	db, _ := assigned(t)
	var fired atomic.Int32
	db.OnWriterFault(func() { fired.Add(1) })
	capPages(t, db)
	_, err := db.ApplyScan(context.Background(), time.Now(), bulk(2000, "full"))
	if !errors.Is(err, ErrWriterFault) || !db.WriterFaulted() || fired.Load() != 1 {
		t.Fatalf("ApplyScan past max_page_count: %v (faulted %v, hook %d)", err, db.WriterFaulted(), fired.Load())
	}
}

// A writer waiting for the writer connection when another write faults is refused once it
// gets the connection: the flag is set before the connection is given back. Its rows are
// unchanged and the hook runs once.
var errNeverWaited = errors.New("the second writer never waited for the connection")

func TestAWriterWaitingForTheConnectionIsRefusedAfterAFault(t *testing.T) {
	db, before := assigned(t)
	var fired atomic.Int32
	db.OnWriterFault(func() { fired.Add(1) })
	capPages(t, db)
	ctx := context.Background()
	second := make(chan error, 1)
	err := db.writeTx(ctx, func(tx *sql.Tx) error {
		go func() { _, err := db.ApplyScan(ctx, time.Now(), bulk(1, "second")); second <- err }()
		// A hang guard only, never a timing bound. It returns an error rather than calling
		// t.Fatal here, which would leave the transaction open and hang the cleanup.
		hang := time.Now().Add(10 * time.Second)
		for db.w.Stats().WaitCount == 0 { // the second writer is blocked in Conn
			if time.Now().After(hang) {
				return errNeverWaited
			}
			time.Sleep(time.Millisecond)
		}
		for _, u := range bulk(2000, "first") {
			if err := u[0].apply(ctx, tx, time.Now()); err != nil {
				return err
			}
		}
		return errors.New("the first write didn't fill the database")
	})
	if !errors.Is(err, ErrWriterFault) {
		t.Fatalf("the first write: %v, want ErrWriterFault", err)
	}
	if err := <-second; !errors.Is(err, ErrWriterFault) {
		t.Fatalf("the waiting write: %v, want ErrWriterFault", err)
	}
	if rows := rowsByPath(t, db); len(rows) != len(before) || fired.Load() != 1 {
		t.Fatalf("%d rows (want %d), hook ran %d times (want 1)", len(rows), len(before), fired.Load())
	}
}

// A busy database (another connection holds the write lock) fails the write, but isn't a
// writer fault.
func TestABusyWriteIsNotAWriterFault(t *testing.T) {
	db, dir := newDB(t)
	var fired atomic.Int32
	db.OnWriterFault(func() { fired.Add(1) })
	if _, err := db.w.Exec("PRAGMA busy_timeout = 50"); err != nil {
		t.Fatal(err)
	}
	other, err := openPool(filepath.Join(dir, AppDB), writerQuery, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	_, err = db.ApplyScan(context.Background(), time.Now(), bulk(1, "busy"))
	if err == nil || errors.Is(err, ErrWriterFault) || db.WriterFaulted() || fired.Load() != 0 {
		t.Fatalf("a busy write: %v (faulted %v, hook %d); want a plain error", err, db.WriterFaulted(), fired.Load())
	}
}

// Close after a fault doesn't force a checkpoint into the faulted file: with a reader holding
// the WAL, a checkpoint would report ErrCheckpointBusy; skipped, Close returns nil and the
// WAL replays at the next open.
func TestCloseAfterAFaultSkipsTheCheckpoint(t *testing.T) {
	db, dir := newDB(t)
	if err := db.Assign(context.Background(), anAssignment(time.Now())); err != nil {
		t.Fatal(err)
	}
	capPages(t, db)
	if _, err := db.ApplyScan(context.Background(), time.Now(), bulk(2000, "full")); !errors.Is(err, ErrWriterFault) {
		t.Fatalf("the fault: %v", err)
	}
	reader, err := openPool(filepath.Join(dir, AppDB), readerQuery, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM files").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close after a fault: %v, want no checkpoint and no error", err)
	}
}
