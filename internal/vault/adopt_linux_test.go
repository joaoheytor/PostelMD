//go:build linux

package vault

import (
	"errors"
	"os"
	"os/exec"
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
			if !errors.Is(err, ErrForeignReserved) || !strings.Contains(err.Error(), reserved+" in your notes folder") ||
				!strings.Contains(err.Error(), markerFix) || !strings.Contains(err.Error(), "rename "+reserved) {
				t.Fatalf("Open = %v, want ErrForeignReserved naming %s, the marker fix and the rename", err, reserved)
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

// markerFix is the command the refusal gives for folders PostelMD made before the marker.
const markerFix = "mkdir -p .postelmd-tmp && touch .postelmd-tmp/.created-by-postelmd"

// TestUpgradeFromABuildWithoutTheMarker: a vault a pre-marker build used has PostelMD's own
// folders but no marker. With deleted notes in the trash it is refused, and the error leads
// with the marker fix and warns that renaming the trash would make its notes ordinary files;
// after that fix it starts with the trash intact. With an empty trash and temp folder it
// starts without help, on any start, not only the first.
func TestUpgradeFromABuildWithoutTheMarker(t *testing.T) {
	dir := t.TempDir()
	trashed := TrashDir + "/20261101T120000.000000000Z/old.md"
	writeFiles(t, dir, map[string]string{"a.md": "a", trashed: "deleted on main"})
	must(t, os.Mkdir(filepath.Join(dir, TmpDir), 0o700))
	_, err := Open(dir)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	fix := strings.Index(msg, markerFix)
	if !errors.Is(err, ErrForeignReserved) || fix < 0 || fix > strings.Index(msg, "rename") ||
		!strings.Contains(msg, "visible") {
		t.Fatalf("Open = %v; want the marker fix first, then the rename and its risk", err)
	}
	cmd := exec.Command("sh", "-c", markerFix) //nolint:gosec // G204: the test's own constant
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the fix as printed: %v\n%s", err, out)
	}
	v, err := Open(dir)
	must(t, err)
	items, _, err := v.ListTrash(10)
	_ = v.Close()
	if err != nil || len(items) != 1 || content(t, dir, trashed) != "deleted on main" {
		t.Fatalf("after the fix: trash %v, %v", items, err)
	}

	empty := t.TempDir()
	writeFiles(t, empty, map[string]string{"a.md": "a"})
	for _, d := range []string{TmpDir, TrashDir} {
		must(t, os.Mkdir(filepath.Join(empty, d), 0o700))
	}
	for start := range 2 {
		v, err := Open(empty)
		if err != nil {
			t.Fatalf("start %d of a vault with empty reserved folders: %v", start, err)
		}
		_ = v.Close()
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
