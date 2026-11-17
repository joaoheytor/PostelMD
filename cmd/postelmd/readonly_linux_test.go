//go:build linux

package main

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

// A writer fault's hook puts the vault read-only and logs it once, as an error record with
// the event "read_only" and no path (ADR-0037).
func TestAWriterFaultPutsTheVaultReadOnly(t *testing.T) {
	dir := t.TempDir()
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	var logs strings.Builder
	hook := goReadOnly(v, slog.New(slog.NewJSONHandler(&logs, nil)))
	hook()
	hook()
	if !v.ReadOnly() {
		t.Fatal("the vault isn't read-only after the hook")
	}
	if n := strings.Count(logs.String(), `"event":"read_only"`); n != 1 || !strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Fatalf("want one error record with event read_only, got %d: %s", n, logs.String())
	}
	if strings.Contains(logs.String(), dir) {
		t.Fatalf("the record names the vault's path: %s", logs.String())
	}
}
