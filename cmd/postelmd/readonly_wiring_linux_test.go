//go:build linux

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	pmhttp "github.com/joaoheytor/PostelMD/internal/http"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	"github.com/joaoheytor/PostelMD/internal/store/lock"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// faultData makes TestReadOnlyWiringHelper run, on that data directory: RLIMIT_FSIZE is
// process-wide, so the test that sets it runs alone in a re-run of this test binary.
var faultData = flag.String("postelmd.fault-data", "", "data directory (read-only wiring helper only)")

// TestReadOnlyWiringHelper opens the instance as serve does (openInstance under the instance
// lock), limits file sizes so the next large database write fails with SQLITE_IOERR, writes,
// and checks the vault went read-only, the log has the read_only record, and the router's
// /readyz says so.
func TestReadOnlyWiringHelper(t *testing.T) {
	if *faultData == "" {
		t.Skip("run by TestADatabaseWriteFaultMakesServeReadOnly")
	}
	paths, _, err := layout.Prepare(*faultData, layout.Options{OpensDatabase: true, AllowEphemeralData: true})
	if err != nil {
		t.Fatal(err)
	}
	lk, err := lock.Acquire(paths.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lk.Release() }()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	in, err := openInstance(context.Background(), paths, lk.BootUUID(), log)
	if err != nil {
		t.Fatal(err)
	}
	defer in.close(log, false)
	limit := largestFile(t, paths.State) + 64<<10
	lim := syscall.Rlimit{Cur: uint64(limit), Max: uint64(limit)} //nolint:gosec // G115: a positive file size
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
		t.Fatal(err)
	}
	a := sqlstore.Assignment{VaultUUID: "6f1c2a7e-3b4d-4c5e-8f90-a1b2c3d4e5f6", Now: time.Now()}
	for i := range 4000 {
		a.Files = append(a.Files, sqlstore.NewFile{Path: fmt.Sprintf("bulk/%04d-%s.md", i, strings.Repeat("x", 200)),
			SHA256: bytes.Repeat([]byte{byte(i)}, 32), Stat: sqlstore.FileStat{Size: 1, Inode: uint64(1000 + i)}}) //nolint:gosec // G115: a small count
	}
	if err := in.db.(*sqlstore.DB).Assign(context.Background(), a); err == nil {
		t.Fatal("the write past the file size limit succeeded")
	}
	if !in.vault.ReadOnly() {
		t.Fatal("the vault isn't read-only after the database write fault")
	}
	if !strings.Contains(logs.String(), `"event":"read_only"`) {
		t.Fatalf("no read_only record: %s", logs.String())
	}
	rec := httptest.NewRecorder()
	pmhttp.NewRouter(in.auth, in.vault, in.ids, fstest.MapFS{}, pmhttp.Build{}, log).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "read-only\n" {
		t.Fatalf("/readyz = %d %q, want 200 \"read-only\"", rec.Code, rec.Body.String())
	}
	t.Log("read-only as expected")
}

// largestFile returns the size of the largest file in dir.
func largestFile(t *testing.T, dir string) int64 {
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

// A database write fault while serving puts the vault read-only, end to end: the instance
// serve opens wires the database's fault hook to the vault, and the router's /readyz reports
// it (ADR-0037).
func TestADatabaseWriteFaultMakesServeReadOnly(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReadOnlyWiringHelper$", "-test.count=1", "-test.v", //nolint:gosec // G204: this test binary, with its own flags
		"-postelmd.fault-data="+data)
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "read-only as expected") {
		t.Fatalf("the helper: %v\n%s", err, out)
	}
}
