//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/config"
)

// A damaged app.db's refusal: TestServeDamagedDatabaseNamesDBRebuildAtFormat2.

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
