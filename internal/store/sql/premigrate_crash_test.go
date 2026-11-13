//go:build linux

package sql

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// fileSizeLimit makes the pre-migration helper run under RLIMIT_FSIZE: a write past it fails
// (EFBIG; Go ignores SIGXFSZ), as on a full disk.
var fileSizeLimit = flag.Uint64("sql.fsize", 0, "RLIMIT_FSIZE in bytes (pre-migration helper only)")

// TestPreMigrateHelper opens a database at the build's version with two later migrations,
// and is either SIGKILLed at -sql.crash-at or run out of space with -sql.fsize.
func TestPreMigrateHelper(t *testing.T) {
	if *crashDir == "" {
		t.Skip("run by TestPreMigrateCrashPoints and TestPreMigrateDiskFull")
	}
	fsys, _ := laterMigrations(t, 2)
	if *fileSizeLimit != 0 {
		lim := syscall.Rlimit{Cur: *fileSizeLimit, Max: *fileSizeLimit}
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
			t.Fatal(err)
		}
		db, err := open(context.Background(), *crashDir, discard, fsys)
		if err == nil {
			_ = db.Close()
			t.Fatal("the migration succeeded past the file size limit")
		}
		if !strings.Contains(err.Error(), "copy the database") { // Snapshot's VACUUM INTO, not another write
			t.Fatalf("failed elsewhere: %v", err)
		}
		t.Logf("refused as expected: %v", err)
		return
	}
	name, nth, _ := strings.Cut(*crashAt, ":")
	want, _ := strconv.Atoi(nth)
	seen := 0
	killPoint = func(p string) {
		if p == name {
			if seen++; seen == want {
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
				select {} // the signal is on its way
			}
		}
	}
	db, err := open(context.Background(), *crashDir, discard, fsys)
	if err == nil {
		_ = db.Close()
	}
	t.Fatalf("not killed at %s (open: %v)", *crashAt, err)
}

// runHelper runs TestPreMigrateHelper on dir with extra flags.
func runHelper(t *testing.T, dir string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestPreMigrateHelper$", "-test.count=1", //nolint:gosec // G204: this test binary, with its own flags
		"-sql.crash-dir=" + dir}, args...)...)
	return cmd.CombinedOutput()
}

// onDisk is what a crash left: the intent, the committed version, the snapshot and its temp.
type onDisk struct {
	intent, snap, tmp bool
	uv                int // relative to from
}

func readOnDisk(t *testing.T, dir string, from, to int) onDisk {
	t.Helper()
	exists := func(n string) bool { _, err := os.Lstat(filepath.Join(dir, n)); return err == nil }
	return onDisk{
		intent: exists(MigrateIntent),
		snap:   exists(snapshotName(from, to)),
		tmp:    exists(snapshotName(from, to) + ".tmp"),
		uv:     committedVersion(t, dir) - from,
	}
}

// Every crash point of the pre-migration path (premigrate.go), with the ADR-0015 row the
// crash leaves and the next open must act on. Migrating from → from+2, so the "between" row
// (from+1) is reached.
func TestPreMigrateCrashPoints(t *testing.T) {
	row1 := onDisk{intent: true}                    // from, snapshot absent
	row1tmp := onDisk{intent: true, tmp: true}      // from, snapshot absent, a temp
	row2 := onDisk{intent: true, snap: true}        // from, snapshot present
	row3 := onDisk{intent: true, snap: true, uv: 1} // between, snapshot present
	row4 := onDisk{intent: true, snap: true, uv: 2} // to, snapshot present
	for _, c := range []struct {
		at   string
		want onDisk
	}{
		{"intent-synced:1", onDisk{}}, // the intent's temp only: nothing started
		{"intent-written:1", row1},
		{"copied:1", row1tmp},
		{"snapshot-verified:1", row1tmp},
		{"snapshot-renamed:1", row2},
		{"snapshot-synced:1", row2},
		{"before-sql:1", row2}, {"before-version:1", row2}, {"before-record:1", row2}, {"before-commit:1", row2},
		{"after-commit:1", row3},
		{"before-sql:2", row3}, {"before-version:2", row3}, {"before-record:2", row3}, {"before-commit:2", row3},
		{"after-commit:2", row4},
		{"marked-db:1", row4},
		{"marked:1", row4},
		{"intent-removed:1", onDisk{snap: true, uv: 2}}, // done
	} {
		t.Run(c.at, func(t *testing.T) {
			dir := closedDB(t)
			fsys, from := laterMigrations(t, 2)
			out, err := runHelper(t, dir, "-sql.crash-at="+c.at)
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
			}
			if got := readOnDisk(t, dir, from, from+2); got != c.want {
				t.Fatalf("after the crash %+v, want %+v", got, c.want)
			}
			db, err := open(context.Background(), dir, discard, fsys)
			if err != nil {
				t.Fatalf("the next open: %v", err)
			}
			defer func() { _ = db.Close() }()
			assertMigrated(t, db, dir, from, from+2)
		})
	}
}

// A full disk while the snapshot is copied (simulated with RLIMIT_FSIZE: EFBIG, which SQLite
// reports as SQLITE_IOERR_WRITE; ENOSPC would be SQLITE_FULL, through the same error path,
// and needs a small filesystem a test can't mount): the open fails, the partial copy is removed, the
// intent stays and the database is unchanged; the next open with space takes the snapshot
// again and migrates (ADR-0015's first row).
func TestPreMigrateDiskFull(t *testing.T) {
	dir := closedDB(t)
	// About 1 MiB of rows, so the copy outgrows the limit while app.db's -shm (32 KiB) and
	// the intent fit under it.
	rawSet(t, dir, "WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM c WHERE i < 2000) "+
		"INSERT INTO meta (key, value) SELECT 'k' || i, hex(randomblob(256)) FROM c")
	fsys, from := laterMigrations(t, 2)
	if out, err := runHelper(t, dir, "-sql.fsize=262144", "-test.v"); err != nil || !strings.Contains(string(out), "refused as expected") {
		t.Fatalf("the helper: %v\n%s", err, out)
	}
	if got := readOnDisk(t, dir, from, from+2); got != (onDisk{intent: true}) {
		t.Fatalf("after the full disk %+v, want the intent only", got)
	}
	db, err := open(context.Background(), dir, discard, fsys)
	if err != nil {
		t.Fatalf("the next open: %v", err)
	}
	defer func() { _ = db.Close() }()
	assertMigrated(t, db, dir, from, from+2)
	var n int
	if err := db.w.QueryRow("SELECT count(*) FROM meta WHERE key LIKE 'k%'").Scan(&n); err != nil || n != 2000 {
		t.Fatalf("rows after the migration: %d, %v", n, err)
	}
}
