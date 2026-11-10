//go:build linux

package vault

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFiles(t *testing.T) {
	v, dir := newVault(t)
	for _, f := range []string{"ops/db/failover.md", "ops/runbook.md", "b.txt", ".git/config", ".postelmd-trash/old.md"} {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o700))
		must(t, os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600))
	}
	must(t, os.Symlink(filepath.Join(dir, "b.txt"), filepath.Join(dir, "link.md")))
	must(t, os.Symlink(filepath.Join(dir, "ops"), filepath.Join(dir, "linked-dir")))

	files, truncated, err := v.Files(100)
	must(t, err)
	want := []string{"b.txt", "notes/a.md", "ops/db/failover.md", "ops/runbook.md"}
	if !slices.Equal(files, want) || truncated {
		t.Fatalf("Files = %v (truncated %v), want %v: reserved paths and symlinks are never listed", files, truncated, want)
	}

	files, truncated, err = v.Files(2)
	must(t, err)
	if len(files) != 2 || !truncated {
		t.Fatalf("Files(2) = %v (truncated %v), want 2 paths and truncated", files, truncated)
	}
}
