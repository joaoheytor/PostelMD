//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// serve's startup order (ADR-0033): open the vault without sweeping, the first upgrade, intent
// recovery, the sweep, then the scan. A save whose rename landed before a crash is rolled
// forward by its intent, not taken by the scan for an outside edit (which would leave the
// intent stale); the sweep spares a flagged intent's temp and removes the rest.
func TestServeRecoversIntentsBeforeTheSweepAndTheScan(t *testing.T) {
	const opA, opB = "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	data := v01Data(t, false)
	notes := vaultOf(data)
	start(t, data).shutdown(t) // the first upgrade: a.md at generation 1
	ctx := context.Background()
	db, err := sqlstore.Open(ctx, filepath.Join(data, ".postelmd"), slog.New(slog.DiscardHandler))
	must(t, err)
	a, err := db.FileByPath(ctx, "a.md")
	must(t, err)
	sum := sha256.Sum256([]byte("new"))
	p := sqlstore.SaveIntent{FileID: a.ID, Path: "a.md", BaseGeneration: a.Generation, BaseSHA256: a.SHA256, NewSHA256: sum[:], Stat: a.Stat}
	must(t, db.Begin(ctx, opA, p, time.Now()))
	must(t, db.Begin(ctx, opB, p, time.Now()))
	must(t, db.Flag(ctx, opB))
	must(t, db.Close())
	// The crash: opA's rename landed (a.md holds the new bytes, its temp is gone); opB's
	// temp is there; and a temp no intent names.
	must(t, os.WriteFile(filepath.Join(notes, "a.md"), []byte("new"), 0o600))
	tmp := filepath.Join(notes, vault.TmpDir)
	must(t, os.WriteFile(filepath.Join(tmp, opB), []byte("kept for the operator"), 0o600))
	must(t, os.WriteFile(filepath.Join(tmp, "leftover"), []byte("half a save"), 0o600))

	r := start(t, data)
	logs := r.out.String()
	r.shutdown(t)
	if g := generationOf(t, data, "a.md"); g != 2 {
		t.Fatalf("a.md at generation %d, want 2: the save rolled forward, once", g)
	}
	for _, want := range []string{`"msg":"intent recovery","rolled_forward":1`, `"generation_bumps":0`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("the logs lack %s:\n%s", want, logs)
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, opB)); err != nil {
		t.Fatalf("the flagged intent's temp was swept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "leftover")); !os.IsNotExist(err) {
		t.Fatalf("a temp no intent names is still there: %v", err)
	}
	db, err = sqlstore.Open(ctx, filepath.Join(data, ".postelmd"), slog.New(slog.DiscardHandler))
	must(t, err)
	defer func() { _ = db.Close() }()
	if in, err := db.Intents(ctx); err != nil || len(in) != 1 || in[0].OpID != opB || !in[0].Flagged {
		t.Fatalf("intents after the start: %+v, %v; want only the flagged one", in, err)
	}
}
