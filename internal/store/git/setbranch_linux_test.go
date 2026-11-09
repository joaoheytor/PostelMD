//go:build linux

package git

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
)

func TestSetBranchLockfile(t *testing.T) {
	g, dir := newRepo(t)
	c1 := commit(t, g, plumbing.ZeroHash, map[string][]byte{"a.md": []byte("1")})
	c2 := commit(t, g, c1, map[string][]byte{"a.md": []byte("2")})
	if err := g.SetBranch("main", plumbing.ZeroHash, c1); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, ".git", "refs", "heads", "main.lock")

	t.Run("a held lock is never broken", func(t *testing.T) {
		if err := os.WriteFile(lock, []byte("external git\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(lock) })
		if err := g.SetBranch("main", c1, c2); !errors.Is(err, ErrRefLocked) {
			t.Fatalf("SetBranch with a held lock = %v, want ErrRefLocked", err)
		}
		if b, err := os.ReadFile(lock); err != nil || string(b) != "external git\n" {
			t.Fatalf("the other process's lock was changed: %q, %v", b, err)
		}
		if h, _ := g.Branch("main"); h != c1 {
			t.Fatalf("main = %s, want %s", h, c1)
		}
	})
	t.Run("a failed compare removes its own lock", func(t *testing.T) {
		if err := g.SetBranch("main", c2, c1); !errors.Is(err, ErrRefMoved) {
			t.Fatalf("stale old = %v, want ErrRefMoved", err)
		}
		if _, err := os.Lstat(lock); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lock left behind: %v", err)
		}
	})
	t.Run("invalid branch names", func(t *testing.T) {
		for _, name := range []string{"", "a..b", "../escape", "a b", "x.lock/y", "-main"} {
			if err := g.SetBranch(name, plumbing.ZeroHash, c1); err == nil {
				t.Errorf("SetBranch(%q) succeeded", name)
			}
		}
	})
	t.Run("real git agrees", func(t *testing.T) {
		if err := g.SetBranch("main", c1, c2); err != nil {
			t.Fatal(err)
		}
		if got := gitCLI(t, dir, "rev-parse", "refs/heads/main"); got != c2.String() {
			t.Fatalf("git rev-parse main = %s, want %s", got, c2)
		}
	})
}

// TestSetBranchSurvivesEveryCrashPoint moves main from c1 to c2, crashing at every step
// (ADR-0016, "Durability"): after a process kill and after a power loss, the ref is c1 or
// c2, and c2 once SetBranch has returned.
func TestSetBranchSurvivesEveryCrashPoint(t *testing.T) {
	for crashAt := 0; ; crashAt++ {
		g, dir := newRepo(t)
		c1 := commit(t, g, plumbing.ZeroHash, map[string][]byte{"a.md": []byte("1")})
		c2 := commit(t, g, c1, map[string][]byte{"a.md": []byte("2")})
		if err := g.SetBranch("main", plumbing.ZeroHash, c1); err != nil {
			t.Fatal(err)
		}
		disk, err := faultfs.New(filepath.Join(dir, ".git"), crashAt)
		if err != nil {
			t.Fatal(err)
		}
		hooked, err := Open(dir, fs.WithHook(disk))
		if err != nil {
			t.Fatal(err)
		}
		err = hooked.SetBranch("main", c1, c2)
		_ = hooked.Close()
		if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
			t.Fatalf("crash at step %d: %v", crashAt, err)
		}
		completed := !disk.Crashed()
		afterKill, err := os.ReadFile(filepath.Join(dir, ".git", "refs", "heads", "main"))
		if err != nil {
			t.Fatalf("crash at step %d: ref unreadable after a process kill: %v", crashAt, err)
		}
		for model, got := range map[string][]byte{"process kill": afterKill, "power loss": disk.AfterPowerLoss()["refs/heads/main"]} {
			want := map[string]bool{c1.String() + "\n": !completed, c2.String() + "\n": true}
			if !want[string(got)] {
				t.Errorf("crash at step %d, after a %s: ref = %q (completed %v)", crashAt, model, got, completed)
			}
		}
		if completed {
			if crashAt != 4 { // create, sync-file, rename, sync-dir
				t.Errorf("SetBranch took %d steps, want 4", crashAt)
			}
			return
		}
	}
}
