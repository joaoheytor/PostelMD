//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/config"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// TestServeDamagedDatabaseNamesAWorkingFix: a damaged app.db is refused, unchanged, and the
// refusal names a fix this build has: every `postelmd <command>` it mentions is one of this
// binary's commands (db rebuild arrives with format 2; until then, the files move aside).
func TestServeDamagedDatabaseNamesAWorkingFix(t *testing.T) {
	data := t.TempDir()
	state := filepath.Join(data, ".postelmd")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	junk := bytes.Repeat([]byte("not a database "), 512)
	if err := os.WriteFile(filepath.Join(state, "app.db"), junk, 0o600); err != nil {
		t.Fatal(err)
	}
	err := serve(context.Background(), config.Config{DataDir: data, Listen: "127.0.0.1:0"}, &syncBuffer{}, nil)
	if !errors.Is(err, sqlstore.ErrDamaged) {
		t.Fatalf("serve on a damaged app.db = %v, want ErrDamaged", err)
	}
	for _, m := range regexp.MustCompile("postelmd ([a-z-]+)").FindAllStringSubmatch(err.Error(), -1) {
		if !regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(m[1]) + ` `).MatchString(usage) {
			t.Errorf("the refusal names `postelmd %s`, which this build doesn't have: %v", m[1], err)
		}
	}
	// The same fix as the runbook's: into a new folder inside .postelmd, never deleted.
	for _, want := range []string{"app.db-wal", "app.db-shm", "never delete", "into a new folder", ".postelmd/damaged-<date>/"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal doesn't say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "out of") {
		t.Errorf("the refusal says to move the files out of .postelmd, unlike its example: %v", err)
	}
	if b, rerr := os.ReadFile(filepath.Join(state, "app.db")); rerr != nil || !bytes.Equal(b, junk) {
		t.Fatalf("the damaged file was changed: %v", rerr)
	}
}

// TestServeStoppedDuringStartupExitsCleanly: a stop signal while the server starts (here,
// right after the listener is up) ends serve without an error and before the database is
// created, so a `docker stop` during a long start isn't reported as a failure.
func TestServeStoppedDuringStartupExitsCleanly(t *testing.T) {
	data := t.TempDir()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	afterListen = func(string) { stop() }
	t.Cleanup(func() { afterListen = nil })
	if err := serve(ctx, config.Config{DataDir: data, Listen: "127.0.0.1:0"}, &syncBuffer{}, nil); err != nil {
		t.Fatalf("serve stopped during startup = %v, want nil", err)
	}
	if _, err := os.Lstat(filepath.Join(data, ".postelmd", "app.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("app.db after a stop during startup = %v, want none", err)
	}
}
