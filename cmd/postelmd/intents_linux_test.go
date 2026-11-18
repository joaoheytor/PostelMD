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

	"github.com/joaoheytor/PostelMD/internal/store/lock"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

const intentA, intentB = "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"

// flaggedData is a data directory at format 2 with a flagged save intent intentA, whose temp
// holds "an edit", and an open one intentB, both on a.md.
func flaggedData(t *testing.T) (data, temp string) {
	t.Helper()
	data = v01Data(t, false)
	start(t, data).shutdown(t) // the first upgrade
	ctx := context.Background()
	db, err := sqlstore.Open(ctx, filepath.Join(data, ".postelmd"), slog.New(slog.DiscardHandler))
	must(t, err)
	a, err := db.FileByPath(ctx, "a.md")
	must(t, err)
	p := sqlstore.SaveIntent{FileID: a.ID, Path: "a.md", BaseGeneration: a.Generation, BaseSHA256: a.SHA256, NewSHA256: make([]byte, 32), Stat: a.Stat}
	must(t, db.Begin(ctx, intentA, p, time.Now()))
	must(t, db.Flag(ctx, intentA))
	must(t, db.Begin(ctx, intentB, p, time.Now()))
	must(t, db.Close())
	temp = filepath.Join(vaultOf(data), vault.TmpDir, intentA)
	must(t, os.WriteFile(temp, []byte("an edit"), 0o600))
	return data, temp
}

// intentsCmd runs postelmd with args on data, returning stdout, stderr and the exit code.
func intentsCmd(data string, args ...string) (string, string, int) {
	var out, errs strings.Builder
	code := run(append(args, "--data", data), &out, &errs)
	return out.String(), errs.String(), code
}

// stillFlagged fails unless intentA is listed as flagged and its temp still holds its edit.
func stillFlagged(t *testing.T, data, temp string) {
	t.Helper()
	if b, err := os.ReadFile(temp); err != nil || string(b) != "an edit" { //nolint:gosec // G304: the test's own data
		t.Fatalf("the temp: %q, %v", b, err)
	}
	if out, errs, code := intentsCmd(data, "intents", "list"); code != 0 || !strings.Contains(out, intentA+"  save  flagged") {
		t.Fatalf("intents list = %d:\n%s%s", code, out, errs)
	}
}

// postelmd intents list and resolve, with the server stopped: list shows each intent with
// its kind, state, time and copies' folder; resolve refuses an open intent, ends a flagged
// one (its temp copied, then removed), and writes its audit records.
func TestIntentsCommands(t *testing.T) {
	data, temp := flaggedData(t)
	out, _, code := intentsCmd(data, "intents", "list")
	if code != 0 || !strings.Contains(out, intentA+"  save  flagged") || !strings.Contains(out, intentB+"  save  open") {
		t.Fatalf("intents list = %d:\n%s", code, out)
	}
	if _, errs, code := intentsCmd(data, "intents", "resolve", intentB); code != 1 || !strings.Contains(errs, "only a flagged intent") {
		t.Fatalf("resolving an open intent = %d, %s", code, errs)
	}
	out, errs, code := intentsCmd(data, "intents", "resolve", intentA)
	copies := filepath.Join(data, ".postelmd", "recovery", intentA)
	if code != 0 || !strings.Contains(out, `"event":"intent.resolving"`) || !strings.Contains(out, `"event":"intent.resolved"`) ||
		!strings.Contains(out, copies) {
		t.Fatalf("intents resolve = %d:\n%s%s", code, out, errs)
	}
	if b, err := os.ReadFile(filepath.Join(copies, "temp")); err != nil || string(b) != "an edit" { //nolint:gosec // G304: the test's own data
		t.Fatalf("the temp's copy: %q, %v", b, err)
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatalf("the temp is still there: %v", err)
	}
	if out, _, _ := intentsCmd(data, "intents", "list"); strings.Contains(out, intentA) || !strings.Contains(out, intentB) {
		t.Fatalf("after resolve, intents list:\n%s", out)
	}
}

// TestIntentsCommandsNeedTheServerStopped: while the instance lock is held, list and resolve
// refuse, and the temp and the intent stay.
func TestIntentsCommandsNeedTheServerStopped(t *testing.T) {
	data, temp := flaggedData(t)
	lk, err := lock.Acquire(filepath.Join(data, ".postelmd", "instance.lock"))
	must(t, err)
	for _, args := range [][]string{{"intents", "list"}, {"intents", "resolve", intentA}} {
		if out, errs, code := intentsCmd(data, args...); code != 1 || out != "" || !strings.Contains(errs, "stop the server first") {
			t.Fatalf("%v with the lock held = %d:\n%s%s", args, code, out, errs)
		}
	}
	must(t, lk.Release())
	stillFlagged(t, data, temp)
}
