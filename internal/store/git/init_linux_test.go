//go:build linux

package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// initVault runs Init on a fresh vault, with foldsCase replaced by probe when it isn't nil,
// and opens the new repository.
func initVault(t *testing.T, probe func(*fs.Root, string) (bool, error)) (*Repo, string) {
	t.Helper()
	if probe != nil {
		old := foldsCase
		foldsCase = probe
		t.Cleanup(func() { foldsCase = old })
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := fs.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if created, err := Init(root, "tmp"); err != nil || !created {
		t.Fatalf("Init = %v, %v; want a new repository", created, err)
	}
	g, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g, dir
}

// TestInitRecordsCaseFolding: like git init, Init writes core.ignorecase = true when the
// filesystem folds case, so the commit walk matches a nested repository's .git in any case
// there (ADR-0028). The probe is injected: the test machine's filesystem doesn't fold.
func TestInitRecordsCaseFolding(t *testing.T) {
	for _, fold := range []bool{true, false} {
		g, dir := initVault(t, func(*fs.Root, string) (bool, error) { return fold, nil })
		if got, err := g.IgnoreCase(); err != nil || got != fold {
			t.Fatalf("probe says %v: IgnoreCase = %v, %v", fold, got, err)
		}
		if cfg := gitCLI(t, dir, "config", "--local", "--list"); !strings.Contains(cfg, "core.repositoryformatversion=0") {
			t.Fatalf("config = %q, want a valid config git reads", cfg)
		}
	}
}

// TestInitProbesTheRealFilesystem: the real probe agrees with the filesystem under the
// vault, and leaves nothing behind in the new .git or the temp folder.
func TestInitProbesTheRealFilesystem(t *testing.T) {
	g, dir := initVault(t, nil)
	if err := os.WriteFile(filepath.Join(dir, "probe"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, "PROBE"))
	want := err == nil
	if got, err := g.IgnoreCase(); err != nil || got != want {
		t.Fatalf("IgnoreCase = %v, %v; want %v, as the filesystem behaves", got, err, want)
	}
	for _, d := range []string{".git", "tmp"} {
		entries, err := os.ReadDir(filepath.Join(dir, d))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.Contains(strings.ToLower(e.Name()), "probe") {
				t.Fatalf("%s/%s left behind", d, e.Name())
			}
		}
	}
}

// TestInitFailsOnProbeError: a probe that can't tell is an error, never a guess.
func TestInitFailsOnProbeError(t *testing.T) {
	foldsCase = func(*fs.Root, string) (bool, error) { return false, errors.New("EIO") }
	t.Cleanup(func() { foldsCase = probeCaseFold })
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := fs.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := Init(root, "tmp"); err == nil {
		t.Fatal("Init succeeded although the probe failed")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".git after a failed Init: %v, want none", err)
	}
}
