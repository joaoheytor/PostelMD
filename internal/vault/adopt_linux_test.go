//go:build linux

package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAdoptionRefusesForeignReservedFolders: a folder adopted with its own .postelmd-tmp or
// .postelmd-trash entries is refused at the first start (ADR-0020, "An existing notes
// folder"), with an error that names the folder to rename, and nothing in it is touched: the
// temp sweep would otherwise delete the user's files.
func TestAdoptionRefusesForeignReservedFolders(t *testing.T) {
	for _, reserved := range []string{TmpDir, TrashDir} {
		t.Run(reserved, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"a.md": "a", reserved + "/mine.txt": "the user's own file"})
			_, err := Open(dir)
			if !errors.Is(err, ErrForeignReserved) || !strings.Contains(err.Error(), reserved) ||
				!strings.Contains(err.Error(), "Rename "+reserved) {
				t.Fatalf("Open = %v, want ErrForeignReserved naming %s and what to do", err, reserved)
			}
			if got := content(t, dir, reserved+"/mine.txt"); got != "the user's own file" {
				t.Fatalf("the user's file is now %q", got)
			}
			if _, err := os.Lstat(filepath.Join(dir, TmpDir, ownerMarker)); err == nil {
				t.Fatal("a refused folder was marked as PostelMD's")
			}
		})
	}
}

// TestAdoptionAcceptsEmptyReservedFolders: an empty .postelmd-tmp or .postelmd-trash holds
// nothing to lose, so it is adopted and marked.
func TestAdoptionAcceptsEmptyReservedFolders(t *testing.T) {
	dir := t.TempDir()
	must(t, os.Mkdir(filepath.Join(dir, TmpDir), 0o700))
	must(t, os.Mkdir(filepath.Join(dir, TrashDir), 0o700))
	v, err := Open(dir)
	must(t, err)
	_ = v.Close()
	if _, err := os.Lstat(filepath.Join(dir, TmpDir, ownerMarker)); err != nil {
		t.Fatalf("the temp folder isn't marked: %v", err)
	}
}

// TestLaterStartsSweepPostelMDsTemps: once PostelMD made the temp folder, its own leftovers
// (a save, or a repository Init, that a crash interrupted) are removed at the next start, and
// trashed items stay where they are.
func TestLaterStartsSweepPostelMDsTemps(t *testing.T) {
	v, dir := newVault(t)
	if _, err := v.Delete(ctx, "notes/a.md"); err != nil {
		t.Fatal(err)
	}
	_ = v.Close()
	writeFiles(t, dir, map[string]string{TmpDir + "/0123abcd": "half a save", TmpDir + "/repo/HEAD": "x"})
	v, err := Open(dir)
	must(t, err)
	t.Cleanup(func() { _ = v.Close() })
	assertNoTemps(t, dir)
	if items, _, err := v.ListTrash(10); err != nil || len(items) != 1 {
		t.Fatalf("trash after a restart: %v, %v; want 1 item", items, err)
	}
}
