//go:build linux

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// postelmd intents list and resolve, with the server stopped: list shows each intent with
// its kind, state, time and copies' folder; resolve refuses an open intent, ends a flagged
// one (its temp copied, then removed), and writes an intent.resolved audit record.
func TestIntentsCommands(t *testing.T) {
	const opA, opB = "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	data := v01Data(t, false)
	start(t, data).shutdown(t) // the first upgrade
	ctx := context.Background()
	db, err := sqlstore.Open(ctx, filepath.Join(data, ".postelmd"), slog.New(slog.DiscardHandler))
	must(t, err)
	a, err := db.FileByPath(ctx, "a.md")
	must(t, err)
	p := sqlstore.SaveIntent{FileID: a.ID, Path: "a.md", BaseGeneration: a.Generation, BaseSHA256: a.SHA256, NewSHA256: make([]byte, 32), Stat: a.Stat}
	must(t, db.Begin(ctx, opA, p, time.Now()))
	must(t, db.Flag(ctx, opA))
	must(t, db.Begin(ctx, opB, p, time.Now()))
	must(t, db.Close())
	temp := filepath.Join(vaultOf(data), vault.TmpDir, opA)
	must(t, os.WriteFile(temp, []byte("an edit"), 0o600))

	cmd := func(args ...string) (string, string, int) {
		var out, errs strings.Builder
		code := run(append(args, "--data", data), &out, &errs)
		return out.String(), errs.String(), code
	}
	out, _, code := cmd("intents", "list")
	if code != 0 || !strings.Contains(out, opA+"  save  flagged") || !strings.Contains(out, opB+"  save  open") {
		t.Fatalf("intents list = %d:\n%s", code, out)
	}
	if _, errs, code := cmd("intents", "resolve", opB); code != 1 || !strings.Contains(errs, "only a flagged intent") {
		t.Fatalf("resolving an open intent = %d, %s", code, errs)
	}
	out, errs, code := cmd("intents", "resolve", opA)
	copies := filepath.Join(data, ".postelmd", "recovery", opA)
	if code != 0 || !strings.Contains(out, `"event":"intent.resolved"`) || !strings.Contains(out, copies) {
		t.Fatalf("intents resolve = %d:\n%s%s", code, out, errs)
	}
	if b, err := os.ReadFile(filepath.Join(copies, "temp")); err != nil || string(b) != "an edit" { //nolint:gosec // G304: the test's own data
		t.Fatalf("the temp's copy: %q, %v", b, err)
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatalf("the temp is still there: %v", err)
	}
	if out, _, _ := cmd("intents", "list"); strings.Contains(out, opA) || !strings.Contains(out, opB) {
		t.Fatalf("after resolve, intents list:\n%s", out)
	}
}
