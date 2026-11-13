//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/config"
)

// newerData is a data directory a newer PostelMD wrote (format 2), with a temp file the vault
// sweep would remove if the vault opened.
func newerData(t *testing.T) (data, leftover string) {
	t.Helper()
	data = t.TempDir()
	tmp := filepath.Join(data, "vaults", "notes", ".postelmd-tmp")
	for _, d := range []string{filepath.Join(data, ".postelmd"), tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	leftover = filepath.Join(tmp, "newer-build-temp")
	for p, s := range map[string]string{filepath.Join(data, ".postelmd", "format"): "2\n",
		filepath.Join(tmp, ".created-by-postelmd"): "", leftover: "x"} {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return data, leftover
}

// TestOlderBinaryRefusesNewerData: serve and reset-password both refuse data written by a
// newer format, with the fix, before the vault opens (its sweep would change the data).
func TestOlderBinaryRefusesNewerData(t *testing.T) {
	const want = "this data was written by a newer PostelMD (format 2); run that version or newer"
	data, leftover := newerData(t)
	cfg := config.Config{DataDir: data, Listen: "127.0.0.1:0"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // a server that starts stops here
	defer cancel()
	if err := serve(ctx, cfg, io.Discard, nil); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("serve = %v, want %q", err, want)
	}
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := resetPassword(cfg, stdin, io.Discard); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("reset-password = %v, want %q", err, want)
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("the vault was opened and swept: %v", err)
	}
	// The database opens only after the format check (ADR-0030, CLAUDE.md rule 12).
	if _, err := os.Lstat(filepath.Join(data, ".postelmd", "app.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("app.db next to newer data = %v, want none created", err)
	}
}

// TestFirstStartWritesTheFormat: a new data directory gets format 1 at the first start.
func TestFirstStartWritesTheFormat(t *testing.T) {
	data := t.TempDir()
	r := start(t, data)
	r.shutdown(t)
	if b, err := os.ReadFile(filepath.Join(data, ".postelmd", "format")); err != nil || string(b) != "1\n" {
		t.Fatalf("format = %q, %v; want 1", b, err)
	}
}
