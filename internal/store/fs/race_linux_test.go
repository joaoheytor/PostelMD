//go:build linux

package fs

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// swapVault creates a vault where "dir" is atomically exchanged, in a loop, with "swap": a
// symlink to a directory outside the vault holding the same file names with secret
// content. This is the symlink-swap (TOCTOU) attack a check-then-open approach loses.
func swapVault(t *testing.T) (r *Root, vault, outside string, stop func()) {
	t.Helper()
	base := t.TempDir()
	vault = filepath.Join(base, "vault")
	outside = filepath.Join(base, "outside")
	mustDo(t, os.MkdirAll(filepath.Join(vault, "dir"), 0o700))
	mustDo(t, os.MkdirAll(outside, 0o700))
	mustDo(t, os.WriteFile(filepath.Join(vault, "dir", "note.md"), []byte("inside"), 0o600))
	mustDo(t, os.WriteFile(filepath.Join(outside, "note.md"), []byte("SECRET"), 0o600))
	mustDo(t, os.Symlink("../outside", filepath.Join(vault, "swap")))
	r, err := OpenRoot(vault)
	mustDo(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			_ = unix.Renameat2(unix.AT_FDCWD, filepath.Join(vault, "dir"),
				unix.AT_FDCWD, filepath.Join(vault, "swap"), unix.RENAME_EXCHANGE)
		}
	}()
	return r, vault, outside, func() { cancel(); wg.Wait(); _ = r.Close() }
}

func TestReadFileSymlinkSwapRace(t *testing.T) {
	r, _, _, stop := swapVault(t)
	defer stop()
	var reads, symlinkErrs int
	for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline); {
		b, err := r.ReadFile("dir/note.md")
		reads++
		if string(b) == "SECRET" {
			t.Fatalf("read outside the vault after %d reads", reads)
		}
		if err != nil {
			symlinkErrs++
		}
	}
	t.Logf("%d reads during swaps, %d refused (the swap was visible), 0 escaped", reads, symlinkErrs)
	if symlinkErrs == 0 {
		t.Log("warning: no swap was observed; the race was not exercised")
	}
}

// TestSwapHarnessCatchesNaiveRead is the negative control: the same attack against a naive
// filepath.Join + os.ReadFile must leak, or the "0 escaped" results above prove nothing.
func TestSwapHarnessCatchesNaiveRead(t *testing.T) {
	_, vault, _, stop := swapVault(t)
	defer stop()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if b, _ := os.ReadFile(filepath.Join(vault, "dir", "note.md")); string(b) == "SECRET" {
			return // the harness detects an escape
		}
	}
	t.Skip("no naive escape observed in 2s; the harness may not be exercising the race on this machine")
}

func TestRenameNoReplaceSymlinkSwapRace(t *testing.T) {
	r, vault, outside, stop := swapVault(t)
	defer stop()
	attempts := 0
	for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline); attempts++ {
		// Move a file into "dir" while "dir" is being swapped with the outside symlink.
		name := filepath.Join(vault, "victim.md")
		mustDo(t, os.WriteFile(name, []byte("x"), 0o600))
		if err := r.RenameNoReplace("victim.md", "dir/victim.md"); err != nil {
			_ = os.Remove(name)
		}
		// Check before cleaning up: "swap" may currently point outside, so removing
		// swap/victim.md first could delete the evidence of an escape.
		if _, err := os.Lstat(filepath.Join(outside, "victim.md")); err == nil {
			t.Fatalf("a rename wrote outside the vault after %d attempts", attempts)
		}
		// The vault directory is "dir" or "swap" depending on the last exchange.
		_ = os.Remove(filepath.Join(vault, "dir", "victim.md"))
		_ = os.Remove(filepath.Join(vault, "swap", "victim.md"))
	}
	t.Logf("%d renames during swaps, none escaped", attempts)
}

func FuzzReadFile(f *testing.F) {
	for _, seed := range []string{"dir/note.md", "../secret", "link-outside", "dirlink/note.md",
		"escape-dir/secret", "/etc/passwd", "dir/./note.md", "dir/../../secret", "a\x00b", "…/x"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, p string) {
		r, _ := newVault(t)
		b, err := r.ReadFile(p)
		if string(b) == "SECRET" {
			t.Fatalf("ReadFile(%q) read outside the vault", p)
		}
		if err == nil && string(b) != "inside" {
			t.Fatalf("ReadFile(%q) = %q, want only the vault's own file", p, b)
		}
	})
}
