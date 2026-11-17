//go:build linux

package sql

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// faultHelper makes TestWriterFaultHelper run: a re-run of this test binary whose writes past
// the state directory's current sizes fail (RLIMIT_FSIZE is process-wide, so the test that
// sets it runs alone in its own process).
var faultHelper = flag.Bool("sql.fault", false, "run the writer fault helper on -sql.crash-dir")

// TestWriterFaultHelper opens the database in -sql.crash-dir, limits file sizes so the next
// large write fails with SQLITE_IOERR or SQLITE_FULL (EFBIG, as on a full disk), and checks
// that the fault is sticky: the scan's write fails with ErrWriterFault, the hook runs once,
// and the next write is refused without a transaction.
func TestWriterFaultHelper(t *testing.T) {
	if !*faultHelper {
		t.Skip("run by TestAWriterFaultIsStickyAndRunsTheHookOnce")
	}
	ctx := context.Background()
	db, err := Open(ctx, *crashDir, discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var fired int
	db.OnWriterFault(func() { fired++ })
	limit := largest(t, *crashDir) + 64<<10
	lim := syscall.Rlimit{Cur: uint64(limit), Max: uint64(limit)} //nolint:gosec // G115: a positive file size
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
		t.Fatal(err)
	}
	// About 1 MiB of new rows in one transaction: the WAL outgrows the limit.
	var units [][]Change
	for i := range 4000 {
		units = append(units, []Change{InsertFile{Path: fmt.Sprintf("bulk/%04d-%s.md", i, strings.Repeat("x", 200)),
			SHA256: sum(byte(i)), Stat: FileStat{Size: 1, Inode: uint64(1000 + i)}}}) //nolint:gosec // G115: a small count
	}
	_, err = db.ApplyScan(ctx, time.Now(), units)
	if !errors.Is(err, ErrWriterFault) || !db.WriterFaulted() || fired != 1 {
		t.Fatalf("the scan's write: %v (faulted %v, hook ran %d times); want ErrWriterFault, once", err, db.WriterFaulted(), fired)
	}
	if _, err := db.ExpireTombstones(ctx, time.Now()); !errors.Is(err, ErrWriterFault) || fired != 1 {
		t.Fatalf("the next write: %v (hook ran %d times); want ErrWriterFault without another hook", err, fired)
	}
	t.Logf("faulted as expected: %v", err)
}

// largest returns the size of the largest file in dir.
func largest(t *testing.T, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.Size() > n {
			n = fi.Size()
		}
	}
	return n
}

// A write that fails with SQLITE_IOERR or SQLITE_FULL (ADR-0030's I/O errors) is a writer
// fault: it comes back as ErrWriterFault, runs the hook once (serve puts the vault read-only),
// and every later write is refused. The database keeps what it had before.
func TestAWriterFaultIsStickyAndRunsTheHookOnce(t *testing.T) {
	db, dir := newDB(t)
	if err := db.Assign(context.Background(), anAssignment(time.Now())); err != nil {
		t.Fatal(err)
	}
	before := len(rowsByPath(t, db))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWriterFaultHelper$", "-test.count=1", "-test.v", //nolint:gosec // G204: this test binary, with its own flags
		"-sql.fault", "-sql.crash-dir="+dir)
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "faulted as expected") {
		t.Fatalf("the helper: %v\n%s", err, out)
	}
	db, err := Open(context.Background(), dir, discard)
	if err != nil {
		t.Fatalf("the next open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if after := len(rowsByPath(t, db)); after != before || db.WriterFaulted() {
		t.Fatalf("after the fault: %d rows (want %d), faulted %v at the next open", after, before, db.WriterFaulted())
	}
}

// Other failures are not writer faults: a stale unit, a canceled context.
func TestOtherWriteErrorsAreNotWriterFaults(t *testing.T) {
	db, _ := assigned(t)
	var fired int
	db.OnWriterFault(func() { fired++ })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.ApplyScan(ctx, time.Now(), [][]Change{{InsertFile{Path: "x.md", SHA256: sum(9)}}}); err == nil ||
		errors.Is(err, ErrWriterFault) {
		t.Fatalf("a canceled write: %v, want a plain error", err)
	}
	if fired != 0 || db.WriterFaulted() {
		t.Fatalf("a canceled write faulted the writer (hook %d)", fired)
	}
}
