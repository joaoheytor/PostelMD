//go:build linux

package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Stats lists what Survey lists, with the stat Survey records, and StatPath answers "absent"
// for anything /api/v1/files wouldn't list.
func TestStatsAndStatPath(t *testing.T) {
	v, dir := surveyVault(t)
	writeFiles(t, dir, map[string]string{"a.md": "A", "notes/b.md": "BB", TrashDir + "/x/old.md": "trashed"})
	must(t, os.Symlink("a.md", filepath.Join(dir, "link.md")))
	got, err := v.Stats(ctx)
	must(t, err)
	obs, err := v.Survey(ctx)
	must(t, err)
	if len(got) != len(obs) || len(got) != 2 {
		t.Fatalf("Stats = %+v, Survey = %+v", got, obs)
	}
	for _, o := range obs {
		if got[o.Path] != o.Stat {
			t.Fatalf("%q: Stats %+v, Survey %+v", o.Path, got[o.Path], o.Stat)
		}
		if st, ok, err := v.StatPath(o.Path); err != nil || !ok || st != o.Stat {
			t.Fatalf("StatPath(%q) = %+v, %v, %v", o.Path, st, ok, err)
		}
	}
	for _, p := range []string{"link.md", "notes", "missing.md", TrashDir + "/x/old.md", "../a.md", "", "a.md/x"} {
		if _, ok, err := v.StatPath(p); ok || err != nil {
			t.Fatalf("StatPath(%q) = %v, %v; want absent", p, ok, err)
		}
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := v.Stats(stopped); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stats = %v, want context.Canceled", err)
	}
}

// Locked holds the vault mutex: a save waits for it, then goes on.
func TestLockedHoldsTheVaultMutex(t *testing.T) {
	v, _ := newVault(t)
	v.wait = 20 * time.Millisecond
	err := v.Locked(ctx, func() error {
		_, err := v.Create(ctx, "x.md", []byte("x"))
		return err
	})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("a create under Locked = %v, want ErrBusy", err)
	}
	if _, err := v.Create(ctx, "x.md", []byte("x")); err != nil {
		t.Fatalf("a create after Locked: %v", err)
	}
}
