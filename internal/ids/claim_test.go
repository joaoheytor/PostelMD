//go:build linux

package ids

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

func markerPath(dir string) string { return filepath.Join(dir, vault.TmpDir, ".created-by-postelmd") }

// TestCheckClaim: the marker absent, empty or unreadable is checked and claimed; one naming
// the stored UUID is "nothing to do"; one that is a symlink is refused; a database without
// IDs is refused. Nothing changes until Claim.
func TestCheckClaim(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "a"})
	for _, tc := range []struct {
		name   string
		marker func()
	}{
		{"empty", func() {}},
		{"absent", func() { must(t, os.Remove(markerPath(e.dir))) }},
		{"another content", func() { must(t, os.WriteFile(markerPath(e.dir), []byte("not ours\n"), 0o600)) }},
		{"another UUID", func() { must(t, vault.WriteMarker(e.dir, "00000000-0000-4000-8000-000000000000", 1)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(markerPath(e.dir))
			must(t, os.WriteFile(markerPath(e.dir), nil, 0o600))
			tc.marker()
			before, _ := os.ReadFile(markerPath(e.dir))
			c, err := CheckClaim(ctx, e.db, e.dir)
			must(t, err)
			if c.Done || c.VaultUUID != testVaultUUID || c.Sampled != 1 || c.Matched != 1 {
				t.Fatalf("CheckClaim = %+v", c)
			}
			if after, _ := os.ReadFile(markerPath(e.dir)); string(after) != string(before) {
				t.Fatal("CheckClaim changed the marker")
			}
			must(t, Claim(e.dir, c, 1))
			m, err := vault.ReadMarker(e.dir)
			if err != nil || m.VaultUUID != testVaultUUID || m.Format != 1 {
				t.Fatalf("after Claim: %+v, %v", m, err)
			}
			if c, err := CheckClaim(ctx, e.db, e.dir); err != nil || !c.Done {
				t.Fatalf("a second CheckClaim = %+v, %v; want nothing to do", c, err)
			}
		})
	}

	t.Run("another vault's UUID is shown", func(t *testing.T) {
		other := "00000000-0000-4000-8000-000000000000"
		must(t, vault.WriteMarker(e.dir, other, 1))
		c, err := CheckClaim(ctx, e.db, e.dir)
		if err != nil || c.MarkerUUID != other {
			t.Fatalf("CheckClaim = %+v, %v; want the marker's UUID %s", c, err, other)
		}
	})

	for name, mk := range map[string]func(p string) error{
		"a symlink": func(p string) error { return os.Symlink("elsewhere", p) },
		"a folder":  func(p string) error { return os.Mkdir(p, 0o700) },
		"a FIFO":    func(p string) error { return syscall.Mkfifo(p, 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			must(t, os.RemoveAll(markerPath(e.dir)))
			must(t, mk(markerPath(e.dir)))
			if _, err := CheckClaim(ctx, e.db, e.dir); !errors.Is(err, vault.ErrMarkerNotAFile) {
				t.Fatalf("CheckClaim = %v, want ErrMarkerNotAFile", err)
			}
		})
	}

	t.Run("no IDs", func(t *testing.T) {
		state := t.TempDir()
		db, err := sqlstore.Open(ctx, state, slog.New(slog.DiscardHandler))
		must(t, err)
		t.Cleanup(func() { _ = db.Close() })
		if _, err := CheckClaim(ctx, db, e.dir); !errors.Is(err, ErrNoIDs) {
			t.Fatalf("CheckClaim = %v, want ErrNoIDs", err)
		}
	})
}
