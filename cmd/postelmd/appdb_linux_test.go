//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/config"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// getStatus is the status of GET url (the body is dropped).
func getStatus(t *testing.T, url string) (int, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header
}

// bootInLock is the boot UUID the instance lock file records.
func bootInLock(t *testing.T, data string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(data, ".postelmd", "instance.lock"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(` boot=(\S+)`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("no boot UUID in the lock file: %q", b)
	}
	return string(m[1])
}

// openSecond opens the running server's app.db through a second handle, as a second writer
// that slipped past the instance lock would.
func openSecond(t *testing.T, data string) *sqlstore.DB {
	t.Helper()
	db, err := sqlstore.Open(context.Background(), filepath.Join(data, ".postelmd"), discardLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestServeListensBeforeOpeningTheDatabase: the listener starts right after the instance
// lock (ADR-0030, startup order step 1): while the server starts, /healthz answers and
// /readyz is 503 with Retry-After, before app.db exists. Once started, app.db is there,
// 0600, records this start's boot UUID (the one in the lock file, ADR-0014), and /readyz
// answers.
func TestServeListensBeforeOpeningTheDatabase(t *testing.T) {
	data := t.TempDir()
	appDB := filepath.Join(data, ".postelmd", "app.db")
	probed := false
	afterListen = func(addr string) {
		probed = true
		if code, _ := getStatus(t, "http://"+addr+"/healthz"); code/100 != 2 {
			t.Errorf("/healthz while starting = %d, want 2xx", code)
		}
		if code, h := getStatus(t, "http://"+addr+"/readyz"); code != http.StatusServiceUnavailable || h.Get("Retry-After") == "" {
			t.Errorf("/readyz while starting = %d (Retry-After %q), want 503 with Retry-After", code, h.Get("Retry-After"))
		}
		if _, err := os.Lstat(appDB); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("app.db before the listener served = %v, want none yet", err)
		}
	}
	t.Cleanup(func() { afterListen = nil })
	r := start(t, data)
	if !probed {
		t.Fatal("the listener wasn't up before startup went on")
	}
	if code, _ := getStatus(t, r.origin+"/readyz"); code/100 != 2 {
		t.Fatalf("/readyz once started = %d, want 2xx", code)
	}
	fi, err := os.Lstat(appDB)
	if err != nil || fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("app.db once started: %v, %v; want a 0600 file", fi, err)
	}
	got, err := openSecond(t, data).BootUUID(context.Background())
	if want := bootInLock(t, data); err != nil || got != want {
		t.Fatalf("boot UUID in app.db = %q, %v; want the lock file's %q", got, err, want)
	}
}

// TestServeStopsOnSplitBrain: when the boot UUID in app.db changes under a running server (a
// second writer slipped past the instance lock), the server stops serving and exits with an
// error (ADR-0014).
func TestServeStopsOnSplitBrain(t *testing.T) {
	bootCheckEvery = 20 * time.Millisecond
	t.Cleanup(func() { bootCheckEvery = defaultBootCheckEvery })
	data := t.TempDir()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	addr := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, config.Config{DataDir: data, Listen: "127.0.0.1:0"}, &syncBuffer{}, func(a string) { addr <- a })
	}()
	var origin string
	select {
	case a := <-addr:
		origin = "http://" + a
	case err := <-done:
		t.Fatalf("serve: %v", err)
	}
	if err := openSecond(t, data).SetBootUUID(context.Background(), "another-process"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errSplitBrain) {
			t.Fatalf("serve = %v, want errSplitBrain", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve kept running after the boot UUID changed")
	}
	if resp, err := http.Get(origin + "/healthz"); err == nil { //nolint:noctx // a refused connection is the expected answer
		_ = resp.Body.Close()
		t.Fatalf("the server still answers after a split brain: %d", resp.StatusCode)
	}
}
