//go:build linux

package vault

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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
	writeFiles(t, dir, map[string]string{"GIT~1/y.md": "y", "z.md": "z"})
	_, err := v.EnableGit()
	must(t, err)
	gitCLI(t, dir, "-c", "core.protectNTFS=false", "add", "notes/a.md", "GIT~1/y.md", "z.md")
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
		// The commit resets .git/index to HEAD: every valid path stays in it (the walk
		// doesn't stop at the refused name, which sorts first), and the look-alike is left
		// out, so the user's git shows it as one staged deletion, never an empty tree.
		if got := gitCLI(t, dir, "ls-files"); got != "notes/a.md\nz.md" {
			t.Fatalf("on disk %v: index = %q, want notes/a.md and z.md only", onDisk, got)
		}
	}
	// A note's history still works with the look-alike in every commit's tree.
	hist, err := v.History("notes/a.md", 10)
	must(t, err)
	if len(hist) != 3 || hist[0].Message != "edit" || hist[2].Message != "host" {
		t.Fatalf("History(notes/a.md) = %+v; want the two edits and the host's commit", hist)
	}
	if b, err := v.Version("notes/a.md", hist[2].Commit); err != nil || string(b) != "A" {
		t.Fatalf("Version(host commit) = %q, %v; want A", b, err)
	}
}

// TestCraftedDotGitInTheTreeIsCarriedThrough: a tree holding .git/hooks/x and ".." can't
// come from git add, but a crafted push can put one on the branch. PostelMD stays lenient:
// status works and lists neither, a commit keeps both entries byte for byte, and the index
// it writes leaves them out. Only the user's own git removes them.
func TestCraftedDotGitInTheTreeIsCarriedThrough(t *testing.T) {
	v, dir := newVault(t)
	_, err := v.EnableGit()
	must(t, err)
	_, err = v.ImportInitial(ctx, "Ada", "ada@example.invalid")
	must(t, err)
	blob := gitStdin(t, dir, "#!/bin/sh\n", "hash-object", "-w", "--stdin")
	hooks := gitStdin(t, dir, "100755 blob "+blob+"\tx\n", "mktree")
	dotgit := gitStdin(t, dir, "040000 tree "+hooks+"\thooks\n", "mktree")
	root := gitStdin(t, dir, gitCLI(t, dir, "ls-tree", "HEAD")+"\n040000 tree "+dotgit+"\t.git\n040000 tree "+hooks+"\t..\n", "mktree")
	c := gitCLI(t, dir, "commit-tree", root, "-p", "HEAD", "-m", "crafted")
	gitCLI(t, dir, "update-ref", "refs/heads/main", c)
	if got := paths(changes(t, v)); len(got) != 0 {
		t.Fatalf("changes = %v, want none", got)
	}
	writeFiles(t, dir, map[string]string{"notes/a.md": "A2"})
	_, err = commitAll(v, "edit", "Ada", "ada@example.invalid")
	must(t, err)
	tree := gitCLI(t, dir, "ls-tree", "HEAD")
	for _, entry := range []string{"040000 tree " + dotgit + "\t.git", "040000 tree " + hooks + "\t.."} {
		if !strings.Contains(tree, entry) {
			t.Fatalf("the new commit's tree = %q, want %q kept", tree, entry)
		}
	}
	if got := gitCLI(t, dir, "ls-files"); got != "notes/a.md" {
		t.Fatalf("index = %q, want notes/a.md only", got)
	}
	if hist, err := v.History("notes/a.md", 10); err != nil || len(hist) != 2 {
		t.Fatalf("History(notes/a.md) = %+v, %v; want the edit and the import", hist, err)
	}
}

// gitStdin runs the git CLI with stdin and returns its trimmed output.
func gitStdin(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null") // as gitCLI: the developer's git config stays out
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
