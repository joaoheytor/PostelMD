//go:build linux

package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// TestImportCountsWhatItCommits: the count the initial import reports (the setup notice's
// "Imported N files") is the number of files in the commit, whatever else the folder holds:
// symlinks, special files, empty folders, reserved and odd names.
func TestImportCountsWhatItCommits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"symlinks", func(t *testing.T, dir string) {
			must(t, os.Symlink("a.md", filepath.Join(dir, "link.md")))
			must(t, os.Symlink("sub", filepath.Join(dir, "linkdir")))
		}},
		{"a fifo", func(t *testing.T, dir string) { must(t, syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600)) }},
		{"empty folders", func(t *testing.T, dir string) { must(t, os.MkdirAll(filepath.Join(dir, "empty/deeper"), 0o700)) }},
		{"reserved names", func(t *testing.T, dir string) {
			writeFiles(t, dir, map[string]string{"sub/.git/config": "x", "a.sync-conflict-1.md": "x", ".postelmd-trash/old.md": "x"})
		}},
		{"a backslash in a name", func(t *testing.T, dir string) { writeFiles(t, dir, map[string]string{`win\path.md`: "x"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, dir := newVault(t)
			writeFiles(t, dir, map[string]string{"a.md": "a", "sub/b.md": "b"})
			tc.setup(t, dir)
			_, err := v.EnableGit()
			must(t, err)
			imp, err := v.ImportInitial(ctx, "Ada", "ada@example.invalid")
			must(t, err)
			committed := strings.Split(gitCLI(t, dir, "ls-tree", "-r", "--name-only", "HEAD"), "\n")
			if imp.Files != len(committed) || imp.Files < 2 {
				t.Fatalf("ImportInitial = %+v, committed %q", imp, committed)
			}
		})
	}
}

// TestImportCountsUnderTheCommitsLock: the count and the "no commits yet" check come from the
// status the commit itself uses, under one acquisition of the vault mutex, not from an earlier
// one. A file the host adds right after that status is neither committed nor counted.
func TestImportCountsUnderTheCommitsLock(t *testing.T) {
	v, dir := newVault(t)
	writeFiles(t, dir, map[string]string{"a.md": "a", "sub/b.md": "b"})
	_, err := v.EnableGit()
	must(t, err)
	ran := false
	v.afterCheck = func() {
		ran = true
		writeFiles(t, dir, map[string]string{"late.md": "the host"})
	}
	imp, err := v.ImportInitial(ctx, "Ada", "ada@example.invalid")
	must(t, err)
	if !ran {
		t.Fatal("the import didn't count under the commit's lock")
	}
	committed := strings.Split(gitCLI(t, dir, "ls-tree", "-r", "--name-only", "HEAD"), "\n")
	if want := []string{"a.md", "notes/a.md", "sub/b.md"}; !reflect.DeepEqual(committed, want) || imp.Files != len(want) {
		t.Fatalf("ImportInitial = %+v, committed %q, want %q", imp, committed, want)
	}
	if again, err := v.ImportInitial(ctx, "Ada", "ada@example.invalid"); err != nil || again.Files != 0 {
		t.Fatalf("a second import = %+v, %v; want nothing, the repository has a commit", again, err)
	}
}

// TestImportReportsSkippedFiles: files over MaxCommitFile aren't committed; the import says
// so, so the notice doesn't claim the whole folder is in git.
func TestImportReportsSkippedFiles(t *testing.T) {
	v, dir := newVault(t)
	writeFiles(t, dir, map[string]string{"a.md": "a"})
	big, err := os.Create(filepath.Join(dir, "video.mp4"))
	must(t, err)
	must(t, big.Truncate(MaxCommitFile+1)) // sparse: costs no disk
	must(t, big.Close())
	_, err = v.EnableGit()
	must(t, err)
	imp, err := v.ImportInitial(ctx, "Ada", "ada@example.invalid")
	must(t, err)
	if imp.Files != 2 || !reflect.DeepEqual(imp.Skipped, []string{"video.mp4"}) {
		t.Fatalf("ImportInitial = %+v, want a.md and notes/a.md, and video.mp4 skipped", imp)
	}
}
