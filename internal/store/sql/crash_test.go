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
	"testing/fstest"
)

// The crash helper: a re-run of this test binary that opens a database and is SIGKILLed at
// one kill point (CLAUDE.md rule 14, Testing conventions).
var (
	crashAt  = flag.String("sql.crash-at", "", "kill point name:occurrence (crash helper only)")
	crashDir = flag.String("sql.crash-dir", "", "state directory (crash helper only)")
)

// crashMigrations are two migrations, so the kill points inside the chain fire twice.
func crashMigrations(t *testing.T) fstest.MapFS {
	m := embeddedFS(t)
	m["0002_notes.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE crash_probe (x INTEGER) STRICT;")}
	return m
}

func TestCrashHelper(t *testing.T) {
	if *crashAt == "" {
		t.Skip("run by TestMigrationCrashPoints")
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
	db, err := open(context.Background(), *crashDir, discard, crashMigrations(t))
	if err == nil {
		_ = db.Close()
	}
	t.Fatalf("not killed at %s (open: %v)", *crashAt, err)
}

// Recovery table of the migration runner (ADR-0030: "a crash mid-migration leaves the
// version before it; the next start runs it again"):
//
//	| Killed at                          | On disk                       | Next open            |
//	|------------------------------------|-------------------------------|----------------------|
//	| created (file made, SQLite not yet) | empty 0600 file              | migrates from 0      |
//	| checked (probed, writer not open)  | empty 0600 file               | migrates from 0      |
//	| opened (writer open, WAL mode set, | empty database in WAL mode,   | migrates from 0      |
//	| no transaction yet)                | user_version 0                |                      |
//	| before-sql, before-version,        | uncommitted transaction;      | rolled back; migrates|
//	| before-record (each migration),    | user_version 0, no records    | from 0               |
//	| before-commit                      |                               |                      |
//	| after-commit                       | user_version 2, two records   | nothing to do        |
func TestMigrationCrashPoints(t *testing.T) {
	for _, c := range []struct {
		at        string
		committed bool
	}{
		{"created:1", false}, {"checked:1", false}, {"opened:1", false},
		{"before-sql:1", false}, {"before-version:1", false}, {"before-record:1", false},
		{"before-sql:2", false}, {"before-version:2", false}, {"before-record:2", false},
		{"before-commit:1", false},
		{"after-commit:1", true},
	} {
		t.Run(c.at, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$", "-test.count=1", //nolint:gosec // G702: this test binary, with its own flags
				"-sql.crash-at="+c.at, "-sql.crash-dir="+dir)
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
			}
			if fi, err := os.Stat(filepath.Join(dir, AppDB)); err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("after the crash: %v, %v", fi, err)
			}
			if got := committedVersion(t, dir); (got == 2) != c.committed || (got != 0 && got != 2) {
				t.Fatalf("after the crash user_version is %d (committed: %v)", got, c.committed)
			}
			db, err := open(context.Background(), dir, discard, crashMigrations(t))
			if err != nil {
				t.Fatalf("the next open: %v", err)
			}
			defer func() { _ = db.Close() }()
			uv, recs := state(t, db)
			if uv != 2 || len(recs) != 2 {
				t.Fatalf("after recovery: user_version %d, records %v", uv, recs)
			}
			if _, err := db.w.Exec("INSERT INTO crash_probe (x) VALUES (1)"); err != nil {
				t.Fatalf("migration 2's table: %v", err)
			}
		})
	}
}

// committedVersion reads user_version from a read-only connection, which sees committed
// transactions only (it can't replay or roll back anything).
func committedVersion(t *testing.T, dir string) int {
	t.Helper()
	p, err := openPool(filepath.Join(dir, AppDB), "mode=ro", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	var v int
	if err := p.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
