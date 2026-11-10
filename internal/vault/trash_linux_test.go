//go:build linux

package vault

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
)

// TestDeleteKeepsLongNames: an item whose name is as long as the filesystem allows (255
// bytes) moves to the trash under its full, unchanged name, in a folder of its own named
// after the time of the delete, so a restore can show and restore that name.
func TestDeleteKeepsLongNames(t *testing.T) {
	multibyte := strings.Repeat("é", 126) + ".md" // 2-byte runes: 255 bytes
	for _, tc := range []struct {
		name, path, file string // file: the content's path below path, for a folder
	}{
		{"255-byte ASCII name", strings.Repeat("a", 252) + ".md", ""},
		{"255-byte name of multibyte runes", multibyte, ""},
		{"255-byte folder name", strings.Repeat("d", 255), "n.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if base := path.Base(tc.path); len(base) != 255 || !utf8.ValidString(base) {
				t.Fatalf("bad fixture: %d bytes", len(base))
			}
			v, dir := newVault(t)
			file := tc.path
			if tc.file != "" {
				must(t, os.Mkdir(filepath.Join(dir, tc.path), 0o700))
				file = tc.path + "/" + tc.file
			}
			must(t, os.WriteFile(filepath.Join(dir, file), []byte("long"), 0o600))
			name, err := v.Delete(ctx, tc.path)
			must(t, err)
			if got := path.Base(name); got != tc.path {
				t.Fatalf("trashed as %q, want the original name", name)
			}
			if got := content(t, dir, TrashDir+"/"+name+strings.TrimPrefix(file, tc.path)); got != "long" {
				t.Fatalf("trash %s holds %q", name, got)
			}
			if content(t, dir, file) != "<missing>" {
				t.Fatal("the item is still in place")
			}
		})
	}
}

// TestDeleteNeverReusesATrashFolder: deletes in the same instant each get a new folder,
// and a folder already there (an empty one a crash left after its mkdir) is never reused.
func TestDeleteNeverReusesATrashFolder(t *testing.T) {
	v, dir := newVault(t)
	at := time.Date(2026, 11, 10, 14, 30, 5, 123456789, time.UTC)
	v.now = func() time.Time { return at }
	stamp := "20261110T143005.123456789Z"
	must(t, os.MkdirAll(filepath.Join(dir, TrashDir, stamp), 0o700)) // the crash leftover
	writeFiles(t, dir, map[string]string{"b.md": "B"})
	var names []string
	for _, p := range []string{"notes/a.md", "b.md"} {
		name, err := v.Delete(ctx, p)
		must(t, err)
		names = append(names, name)
	}
	if want := []string{stamp + "-1/a.md", stamp + "-2/b.md"}; strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("trashed as %q, want %q", names, want)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, TrashDir, stamp)); err != nil || len(entries) != 0 {
		t.Fatalf("the leftover folder holds %d entries (%v)", len(entries), err)
	}
}

// TestCrashDuringDeleteLeavesAtMostAnEmptyFolder: the recovery table of a delete (ADR-0020),
// at every step, for a process kill (TestCrashDuringDelete covers power loss). The crash
// leaves either the item in place, with at most an empty stamp folder in the trash (crash
// after the mkdir), or the item in its stamp folder (crash after the rename). Nothing needs
// repair: the trash stays out of listings and git, and the next delete in the same instant
// makes a new folder instead of reusing the empty one.
func TestCrashDuringDeleteLeavesAtMostAnEmptyFolder(t *testing.T) {
	at := time.Date(2026, 11, 10, 14, 30, 5, 0, time.UTC)
	stamp := "20261110T143005.000000000Z"
	sawEmpty := false
	for crashAt := 0; ; crashAt++ {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"notes/a.md": "A"})
		must(t, os.Mkdir(filepath.Join(dir, TmpDir), 0o700)) // in the disk's snapshot, as in crashEverywhere
		disk, err := faultfs.New(dir, crashAt)
		must(t, err)
		hook := &armed{disk: disk}
		v, err := Open(dir, fs.WithHook(hook))
		must(t, err)
		v.now = func() time.Time { return at }
		hook.on = true
		_, err = v.Delete(ctx, "notes/a.md")
		_ = v.Close()
		if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
			t.Fatalf("crash at step %d: %v", crashAt, err)
		}

		inPlace := content(t, dir, "notes/a.md") == "A"
		entries, _ := os.ReadDir(filepath.Join(dir, TrashDir))
		var empty, holding int
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), stamp) {
				t.Fatalf("crash at step %d: unexpected trash entry %q", crashAt, e.Name())
			}
			inside, err := os.ReadDir(filepath.Join(dir, TrashDir, e.Name()))
			must(t, err)
			switch {
			case len(inside) == 0:
				empty++
			case len(inside) == 1 && content(t, dir, TrashDir+"/"+e.Name()+"/a.md") == "A":
				holding++
			default:
				t.Fatalf("crash at step %d: trash folder %q holds %d entries", crashAt, e.Name(), len(inside))
			}
		}
		if inPlace == (holding == 1) || holding > 1 || (holding == 1 && empty > 0) {
			t.Fatalf("crash at step %d: in place %v, %d trashed, %d empty folders", crashAt, inPlace, holding, empty)
		}
		sawEmpty = sawEmpty || empty > 0

		// After the restart, the leftover changes nothing the user sees, and isn't reused.
		reopened, err := Open(dir)
		must(t, err)
		reopened.now = func() time.Time { return at }
		if files, _, err := reopened.Files(100); err != nil || strings.Join(files, " ") != map[bool]string{true: "notes/a.md", false: ""}[inPlace] {
			t.Fatalf("crash at step %d: Files = %q, %v", crashAt, files, err)
		}
		if inPlace {
			name, err := reopened.Delete(ctx, "notes/a.md")
			must(t, err)
			if empty > 0 && name != stamp+"-1/a.md" {
				t.Fatalf("crash at step %d: the retry trashed into %q, reusing the leftover", crashAt, name)
			}
		}
		_ = reopened.Close()
		if !disk.Crashed() {
			break
		}
	}
	if !sawEmpty {
		t.Fatal("no crash point left an empty stamp folder: the steps changed, update this test")
	}
}

// TestDeleteOfAMissingItemLeavesNoTrashFolder: a failed delete removes the folder it made.
func TestDeleteOfAMissingItemLeavesNoTrashFolder(t *testing.T) {
	v, dir := newVault(t)
	if _, err := v.Delete(ctx, "notes/gone.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete = %v, want ErrNotFound", err)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, TrashDir)); err != nil || len(entries) != 0 {
		t.Fatalf("the trash holds %d entries (%v)", len(entries), err)
	}
}

// TestLongNamesAreRefusedPlainly: a destination name over 255 bytes is the user's choice; it
// is ErrNameTooLong (a 400), never an internal error. Reading such a path finds nothing.
func TestLongNamesAreRefusedPlainly(t *testing.T) {
	v, _ := newVault(t)
	long := "notes/" + strings.Repeat("x", 253) + ".md" // 256 bytes
	if _, err := v.Create(ctx, long, []byte("N")); !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("Create = %v, want ErrNameTooLong", err)
	}
	if err := v.Mkdir(ctx, strings.Repeat("d", 256)); !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("Mkdir = %v, want ErrNameTooLong", err)
	}
	if err := v.Move(ctx, "notes/a.md", long); !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("Move = %v, want ErrNameTooLong", err)
	}
	if _, _, err := v.Read(long); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read = %v, want ErrNotFound", err)
	}
	if _, err := v.Save(ctx, long, RevOf([]byte("A")), []byte("B")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Save = %v, want ErrNotFound", err)
	}
	if _, err := v.Delete(ctx, long); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete = %v, want ErrNotFound", err)
	}
}
