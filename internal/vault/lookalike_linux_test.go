//go:build linux

package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestGitLookalikesOnDiskAreHidden: names git treats as .git on Windows or macOS (#119),
// made on disk outside PostelMD, are handled like .git itself: never listed, never offered
// for a commit, and the folders around them stay ordinary folders.
func TestGitLookalikesOnDiskAreHidden(t *testing.T) {
	v, dir := newVault(t)
	hfs := ".g" + string(rune(0x200c)) + "it"
	writeFiles(t, dir, map[string]string{".git /x.md": "x", "notes/.git.": "gitdir: elsewhere\n", "GIT~1/y.md": "y",
		hfs + "/z.md": "z", "notes/b.md": "b"})
	_, err := v.EnableGit()
	must(t, err)
	root, err := v.List("")
	must(t, err)
	notes, err := v.List("notes")
	must(t, err)
	if !reflect.DeepEqual(root, []Entry{{Name: "notes", Kind: KindFolder}}) || len(notes) != 2 {
		t.Fatalf("List = %+v and notes %+v; want only the notes folder and its two notes", root, notes)
	}
	files, _, err := v.Files(100)
	must(t, err)
	st, err := v.Status(ctx)
	must(t, err)
	want := []string{"notes/a.md", "notes/b.md"}
	if !reflect.DeepEqual(files, want) || !reflect.DeepEqual(paths(st.Changes), want) || len(st.Repositories) != 0 {
		t.Fatalf("Files = %v, Status = %+v; want %v only and no repository", files, st, want)
	}
}

// TestCommittedLookalikeIsCarriedThrough: a note committed on the host before its name was
// reserved (git~1, which git accepts with core.protectNTFS off) is neither listed as deleted
// nor dropped: the next commit keeps its blob as it is, whether the file is still on disk or
// not. Only the user's own git removes it.
func TestCommittedLookalikeIsCarriedThrough(t *testing.T) {
	v, dir := newVault(t)
	writeFiles(t, dir, map[string]string{"GIT~1/y.md": "y"})
	_, err := v.EnableGit()
	must(t, err)
	gitCLI(t, dir, "-c", "core.protectNTFS=false", "add", "notes/a.md", "GIT~1/y.md")
	gitCLI(t, dir, "-c", "core.protectNTFS=false", "commit", "-q", "-m", "host")
	blob := gitCLI(t, dir, "rev-parse", "HEAD:GIT~1/y.md")
	for _, onDisk := range []bool{true, false} {
		if !onDisk {
			must(t, os.RemoveAll(filepath.Join(dir, "GIT~1")))
		}
		writeFiles(t, dir, map[string]string{"notes/a.md": "A, edited while on disk: " + map[bool]string{true: "yes", false: "no"}[onDisk]})
		if got := paths(changes(t, v)); !reflect.DeepEqual(got, []string{"notes/a.md"}) {
			t.Fatalf("on disk %v: changes = %v, want notes/a.md only", onDisk, got)
		}
		_, err = commitAll(v, "edit", "Ada", "ada@example.invalid")
		must(t, err)
		if got := gitCLI(t, dir, "rev-parse", "HEAD:GIT~1/y.md"); got != blob {
			t.Fatalf("on disk %v: GIT~1/y.md after the commit = %q, want the blob %q kept", onDisk, got, blob)
		}
	}
}
