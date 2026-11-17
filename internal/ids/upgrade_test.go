//go:build linux

package ids

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/layout"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// v01Env is v0.1's data after one start of a format-1 build: a vault with a.md, sub/b.md
// and one trash item, an empty marker, the credentials, no format file (a pre-format v0.1
// directory) and an app.db without file IDs, open: upgrade's step 2 done.
func v01Env(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, dir: filepath.Join(t.TempDir(), "vault"), state: filepath.Join(t.TempDir(), "state"), clock: rebuildNow}
	must(t, os.Mkdir(e.dir, 0o700))
	must(t, os.Mkdir(e.state, 0o700))
	for p, c := range map[string]string{"a.md": "a", "sub/b.md": "b", "gone.md": "g"} {
		e.write(p, c)
	}
	for n, c := range map[string]string{"auth.json": "{}", "secret.key": "k"} {
		must(t, os.WriteFile(filepath.Join(e.state, n), []byte(c), 0o600))
	}
	e.open()
	if _, err := e.v.Delete(ctx, "gone.md"); err != nil {
		t.Fatal(err)
	}
	return e
}

func upgradeOpts(e *env) UpgradeOptions {
	return UpgradeOptions{State: e.state, Vault: e.dir, Format: 1, Now: rebuildNow}
}

// checkUpgraded asserts what a finished first upgrade leaves (ADR-0030, ADR-0041): no
// intent; the flag with vaultUUID, and no rebuild UUID; the marker naming it; a row per
// file and the trash item; a complete pre-format-2/; the format written.
func checkUpgraded(t *testing.T, e *env, vaultUUID string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(e.state, sqlstore.UpgradeIntent)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("upgrade.intent is still there: %v", err)
	}
	id, err := e.db.Identity(ctx)
	must(t, err)
	if !id.Assigned || id.VaultUUID != vaultUUID || id.RebuildUUID != "" {
		t.Fatalf("identity %+v, want the vault %s", id, vaultUUID)
	}
	m, err := vault.ReadMarker(e.dir)
	if err != nil || m.VaultUUID != vaultUUID || m.Format != 1 {
		t.Fatalf("marker %+v, %v; want %s", m, err, vaultUUID)
	}
	e.row("a.md")
	e.row("sub/b.md")
	e.noRow("gone.md")
	if recs, err := e.db.TrashRecords(ctx); err != nil || len(recs) != 1 {
		t.Fatalf("trash records %d, %v; want 1", len(recs), err)
	}
	if _, err := os.Stat(filepath.Join(e.state, layout.PreFormat2, "complete")); err != nil {
		t.Fatalf("pre-format-2/ isn't complete: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(e.state, "format")); err != nil || string(b) != "1\n" { //nolint:gosec // G304: the test's state
		t.Fatalf("format = %q, %v", b, err)
	}
}

// TestUpgrade runs the first upgrade on v0.1 data.
func TestUpgrade(t *testing.T) {
	e := v01Env(t)
	must(t, CheckUpgrade(ctx, e.db, upgradeOpts(e)))
	u, err := Upgrade(ctx, e.db, e.v, upgradeOpts(e))
	must(t, err)
	if u.Resumed || u.Files != 2 || u.Trash != 1 || u.KeptCopy {
		t.Fatalf("Upgrade = %+v", u)
	}
	checkUpgraded(t, e, u.VaultUUID)
	got := []string{}
	es, _ := os.ReadDir(filepath.Join(e.state, layout.PreFormat2))
	for _, x := range es {
		got = append(got, x.Name())
	}
	if !slices.Equal(got, []string{"auth.json", "complete", "secret.key"}) {
		t.Fatalf("pre-format-2/ = %v", got)
	}
}

// TestUpgradeMovesAsideAnEarlierUpgradesCopy: an upgrade, a rollback that left
// pre-format-2/ in place, a password reset, then a fresh upgrade: the earlier copy, with the
// revoked hash, is moved aside, and pre-format-2/ holds today's credentials (ADR-0041).
func TestUpgradeMovesAsideAnEarlierUpgradesCopy(t *testing.T) {
	e := v01Env(t)
	old := filepath.Join(e.state, layout.PreFormat2)
	must(t, os.Mkdir(old, 0o700))
	must(t, os.WriteFile(filepath.Join(old, "auth.json"), []byte("revoked"), 0o600))
	must(t, os.WriteFile(filepath.Join(old, "complete"), []byte(testVaultUUID+" 2026-01-01T00:00:00Z\n"), 0o600))
	u, err := Upgrade(ctx, e.db, e.v, upgradeOpts(e))
	must(t, err)
	if u.KeptCopy {
		t.Fatal("an earlier upgrade's copy was kept")
	}
	checkUpgraded(t, e, u.VaultUUID)
	if b, _ := os.ReadFile(filepath.Join(old, "auth.json")); string(b) != "{}" { //nolint:gosec // G304: the test's state
		t.Fatalf("pre-format-2/auth.json = %q, want today's", b)
	}
	aside := filepath.Join(e.state, layout.PreFormat2+"-"+u.Stamp(), "auth.json")
	if b, err := os.ReadFile(aside); err != nil || string(b) != "revoked" { //nolint:gosec // G304: the test's state
		t.Fatalf("the earlier copy moved aside: %q, %v", b, err)
	}
}

// TestUpgradeStopsOnASymlinkedSource after its intent: step 1 refuses, the intent stays and
// nothing else changes; once the file is fixed, the next start finishes the same upgrade.
func TestUpgradeStopsOnASymlinkedSource(t *testing.T) {
	e := v01Env(t)
	auth := filepath.Join(e.state, "auth.json")
	must(t, os.Remove(auth))
	must(t, os.Symlink(filepath.Join(t.TempDir(), "x"), auth))
	if _, err := Upgrade(ctx, e.db, e.v, upgradeOpts(e)); err == nil {
		t.Fatal("Upgrade copied through a symlinked auth.json")
	}
	in, err := sqlstore.ReadUpgradeIntent(e.state)
	if err != nil || in == nil {
		t.Fatalf("the intent after the refusal: %v, %v", in, err)
	}
	if _, err := os.Lstat(filepath.Join(e.state, layout.PreFormat2)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refusal wrote pre-format-2/: %v", err)
	}
	if id, _ := e.db.Identity(ctx); id.Assigned {
		t.Fatal("the refusal assigned IDs")
	}
	must(t, os.Remove(auth))
	must(t, os.WriteFile(auth, []byte("{}"), 0o600))
	u, err := Upgrade(ctx, e.db, e.v, upgradeOpts(e))
	must(t, err)
	if !u.Resumed || u.VaultUUID != in.VaultUUID {
		t.Fatalf("the next start: %+v, want the intent's upgrade resumed", u)
	}
	checkUpgraded(t, e, in.VaultUUID)
}

// TestUpgradeRefuses what isn't a first upgrade to resume or start, changing nothing.
func TestUpgradeRefuses(t *testing.T) {
	const other = "00000000-0000-4000-8000-000000000000"
	for _, c := range []struct {
		name   string
		setup  func(e *env)
		want   error
		intent bool // the intent was there before, and stays
	}{
		{"flag set without an intent", func(e *env) {
			must(t, e.db.Assign(ctx, sqlstore.Assignment{VaultUUID: testVaultUUID, Now: rebuildNow}))
		}, sqlstore.ErrUpgradeInterference, false},
		{"marker names another vault", func(e *env) {
			must(t, vault.WriteMarker(e.dir, other, 1))
		}, ErrStateNotHere, false},
		{"marker is a folder", func(e *env) {
			p := filepath.Join(e.dir, vault.TmpDir, ".created-by-postelmd")
			must(t, os.Remove(p))
			must(t, os.Mkdir(p, 0o700))
		}, vault.ErrMarkerNotAFile, false},
		{"marker is a symlink", func(e *env) {
			p := filepath.Join(e.dir, vault.TmpDir, ".created-by-postelmd")
			must(t, os.Remove(p))
			must(t, os.Symlink(filepath.Join(t.TempDir(), "x"), p))
		}, vault.ErrMarkerNotAFile, false},
		{"flag set under another UUID than the intent's", func(e *env) {
			_, err := sqlstore.BeginUpgrade(e.state, rebuildNow)
			must(t, err)
			must(t, e.db.Assign(ctx, sqlstore.Assignment{VaultUUID: testVaultUUID, Now: rebuildNow}))
		}, sqlstore.ErrUpgradeInterference, true},
		{"marker names another vault than the intent's", func(e *env) {
			_, err := sqlstore.BeginUpgrade(e.state, rebuildNow)
			must(t, err)
			must(t, vault.WriteMarker(e.dir, other, 1))
		}, ErrAnotherVault, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := v01Env(t)
			c.setup(e)
			before, _ := vault.ReadMarker(e.dir)
			if err := CheckUpgrade(ctx, e.db, upgradeOpts(e)); !errors.Is(err, c.want) {
				t.Fatalf("CheckUpgrade = %v, want %v", err, c.want)
			}
			if _, err := Upgrade(ctx, e.db, e.v, upgradeOpts(e)); !errors.Is(err, c.want) {
				t.Fatalf("Upgrade = %v, want %v", err, c.want)
			}
			if _, err := os.Lstat(filepath.Join(e.state, sqlstore.UpgradeIntent)); (err == nil) != c.intent {
				t.Fatalf("upgrade.intent there = %v, want %v", err == nil, c.intent)
			}
			for _, n := range []string{layout.PreFormat2, "format"} {
				if _, err := os.Lstat(filepath.Join(e.state, n)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("a refused upgrade wrote %s", n)
				}
			}
			if after, _ := vault.ReadMarker(e.dir); after != before {
				t.Fatalf("the marker changed: %+v, was %+v", after, before)
			}
		})
	}
}
