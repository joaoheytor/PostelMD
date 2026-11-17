//go:build linux

package ids

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

const otherVaultUUID = "00000000-0000-4000-8000-000000000000"

// TestCheckVault is ADR-0030's format table at data format 2 (step 5, before the vault
// opens): the marker naming the stored vault UUID goes on; another UUID, a marker that can't
// be read, and a missing or empty marker the identity check can't vouch for are refused,
// changing nothing, each naming its fix; a missing marker with no live rows, or an empty one
// the identity check passes, is rewritten with the stored UUID and a warning.
func TestCheckVault(t *testing.T) {
	notes := map[string]string{"a.md": "a", "sub/b.md": "b"}
	markerPath := func(e *env) string { return filepath.Join(e.dir, vault.TmpDir, ".created-by-postelmd") }
	for _, c := range []struct {
		name     string
		files    map[string]string
		setup    func(e *env)
		want     error  // nil: it goes on
		fix      string // what the refusal names
		rewrites bool   // the marker ends up naming the stored UUID, with a warning
	}{
		{"same UUID", notes, func(e *env) { must(t, vault.WriteMarker(e.dir, testVaultUUID, 2)) }, nil, "", false},
		{"another UUID", notes, func(e *env) { must(t, vault.WriteMarker(e.dir, otherVaultUUID, 2)) },
			ErrAnotherVault, "postelmd db rebuild", false},
		{"unparseable", notes, func(e *env) { must(t, os.WriteFile(markerPath(e), []byte("not ours\n"), 0o600)) },
			vault.ErrBadMarker, "postelmd vault claim", false},
		{"a folder", notes, func(e *env) {
			must(t, os.Remove(markerPath(e)))
			must(t, os.Mkdir(markerPath(e), 0o700))
		}, vault.ErrMarkerNotAFile, "postelmd vault claim", false},
		{"absent, live rows", notes, func(e *env) { must(t, os.Remove(markerPath(e))) },
			ErrVaultNotClaimed, "postelmd vault claim", false},
		{"absent, no live rows", nil, func(e *env) { must(t, os.Remove(markerPath(e))) }, nil, "", true},
		{"empty, the identity check passes", notes, func(*env) {}, nil, "", true},
		{"empty, the identity check fails", notes, func(e *env) {
			e.rm("a.md")
			e.rm("sub/b.md")
			e.write("a.md", "another a")
		}, ErrVaultNotClaimed, "postelmd vault claim", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, c.files)
			c.setup(e)
			before, beforeErr := vault.ReadMarker(e.dir)
			var logs bytes.Buffer
			err := CheckVault(ctx, e.db, e.dir, 2, slog.New(slog.NewJSONHandler(&logs, nil)))
			if !errors.Is(err, c.want) || (c.want == nil) != (err == nil) {
				t.Fatalf("CheckVault = %v, want %v", err, c.want)
			}
			if err != nil && !strings.Contains(err.Error(), c.fix) {
				t.Fatalf("the refusal doesn't name %q: %v", c.fix, err)
			}
			after, afterErr := vault.ReadMarker(e.dir)
			switch {
			case c.rewrites:
				if afterErr != nil || after.VaultUUID != testVaultUUID || after.Format != 2 {
					t.Fatalf("marker %+v, %v; want it rewritten with the stored UUID", after, afterErr)
				}
				if !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), "vault marker") {
					t.Fatalf("no warning for the rewritten marker:\n%s", logs.String())
				}
			case after != before || (afterErr == nil) != (beforeErr == nil):
				t.Fatalf("the marker changed: %+v (%v), was %+v (%v)", after, afterErr, before, beforeErr)
			}
		})
	}
}

// TestCheckVaultWithoutIDs: at data format 2 a database without the "assigned" flag isn't
// this state's (a first upgrade always sets it before writing 2): refused, naming db rebuild.
func TestCheckVaultWithoutIDs(t *testing.T) {
	e := v01Env(t)
	err := CheckVault(ctx, e.db, e.dir, 2, slog.New(slog.DiscardHandler))
	if !errors.Is(err, ErrNoIDsAtFormat2) || !strings.Contains(err.Error(), "postelmd db rebuild") {
		t.Fatalf("CheckVault = %v, want ErrNoIDsAtFormat2 naming db rebuild", err)
	}
	if m, _ := vault.ReadMarker(e.dir); m.VaultUUID != "" {
		t.Fatalf("the marker was written: %+v", m)
	}
}
