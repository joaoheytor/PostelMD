//go:build linux

package ids

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/layout"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

const otherVault = "00000000-0000-4000-8000-000000000000"

// stuckEnv is a first upgrade stopped on interference: its intent, a complete
// pre-format-2/, and what break does to it; the server stopped.
func stuckEnv(t *testing.T, broken func(e *env)) *env {
	t.Helper()
	e := v01Env(t)
	_, err := sqlstore.BeginUpgrade(e.state, rebuildNow)
	must(t, err)
	must(t, os.Mkdir(filepath.Join(e.state, layout.PreFormat2), 0o700))
	must(t, os.WriteFile(filepath.Join(e.state, layout.PreFormat2, "auth.json"), []byte("{}"), 0o600))
	broken(e)
	if _, err := Upgrade(ctx, e.db, e.v, upgradeOpts(e)); err == nil {
		t.Fatal("the upgrade isn't stuck")
	}
	e.close()
	return e
}

// TestRebuildTakesOverAStuckUpgrade: an upgrade that can't finish (IDs, or the marker, under
// another vault UUID than its intent's) is no dead end: db rebuild moves upgrade.intent into
// its damaged folder, leaves pre-format-2/ in place, and rebuilds (ADR-0041).
func TestRebuildTakesOverAStuckUpgrade(t *testing.T) {
	for name, broken := range map[string]func(e *env){
		"IDs under another UUID": func(e *env) {
			must(t, e.db.Assign(ctx, sqlstore.Assignment{VaultUUID: otherVault, Now: rebuildNow}))
		},
		"the marker names another vault": func(e *env) { must(t, vault.WriteMarker(e.dir, otherVault, 1)) },
	} {
		t.Run(name, func(t *testing.T) {
			e := stuckEnv(t, broken)
			r, err := Rebuild(ctx, rebuildOpts(e))
			must(t, err)
			if !r.Upgrade {
				t.Fatalf("Rebuild = %+v, want it to take over the upgrade", r)
			}
			if _, err := os.Lstat(filepath.Join(e.state, sqlstore.UpgradeIntent)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("upgrade.intent is still in the state directory: %v", err)
			}
			if _, err := os.Stat(filepath.Join(e.state, r.DamagedDir(), sqlstore.UpgradeIntent)); err != nil {
				t.Fatalf("upgrade.intent isn't in the damaged folder: %v", err)
			}
			if _, err := os.Stat(filepath.Join(e.state, layout.PreFormat2, "auth.json")); err != nil {
				t.Fatalf("pre-format-2/ didn't stay: %v", err)
			}
			e.open()
			id, err := e.db.Identity(ctx)
			must(t, err)
			m, _ := vault.ReadMarker(e.dir)
			if id.RebuildUUID != r.UUID || m.VaultUUID != id.VaultUUID {
				t.Fatalf("identity %+v, marker %+v: not rebuilt", id, m)
			}
		})
	}
}

// TestRebuildRefusesAResumableUpgrade: an upgrade the next start can finish isn't taken
// over: db rebuild refuses, names the fix, and changes nothing.
func TestRebuildRefusesAResumableUpgrade(t *testing.T) {
	e := v01Env(t)
	u, err := sqlstore.BeginUpgrade(e.state, rebuildNow)
	must(t, err)
	e.close()
	_, err = Rebuild(ctx, RebuildOptions{State: e.state, Vault: e.dir, Format: 1, Now: rebuildNow, Log: slog.New(slog.DiscardHandler)})
	if !errors.Is(err, sqlstore.ErrUpgradeInProgress) {
		t.Fatalf("Rebuild = %v, want ErrUpgradeInProgress", err)
	}
	if in, err := sqlstore.ReadUpgradeIntent(e.state); err != nil || in == nil || *in != u {
		t.Fatalf("the intent after the refusal: %+v, %v", in, err)
	}
	if _, err := os.Lstat(filepath.Join(e.state, sqlstore.RebuildIntent)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refusal wrote rebuild.intent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.state, sqlstore.AppDB)); err != nil {
		t.Fatalf("the refusal moved app.db: %v", err)
	}
}
