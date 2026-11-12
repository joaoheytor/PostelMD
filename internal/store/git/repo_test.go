package git

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

var sig = Signature{Name: "Test", Email: "test@example.invalid", When: time.Unix(1_800_000_000, 0).UTC()}

// newRepo initializes a non-bare vault repository and opens it through the adapter.
func newRepo(t *testing.T) (*Repo, string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := gogit.PlainInit(dir, false); err != nil {
		t.Fatal(err)
	}
	g, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g, dir
}

func commit(t *testing.T, g *Repo, parent Hash, changes map[string][]byte) Hash {
	t.Helper()
	h, err := g.Commit(parent, changes, "test", sig, sig)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// gitCLI runs the real git binary: an independent check that go-git wrote valid objects.
func gitCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCommitFromBytes(t *testing.T) {
	g, dir := newRepo(t)
	// Names chosen so naive sorting differs from git's ("a" the directory sorts as "a/").
	c1 := commit(t, g, plumbing.ZeroHash, map[string][]byte{
		"a/note.md": []byte("nested\n"), "a.md": []byte("file\n"), "a-b.md": []byte("dash\n"),
		"deep/x/y/z.md": []byte("deep\n"),
	})
	if err := g.SetBranch("main", plumbing.ZeroHash, c1); err != nil {
		t.Fatal(err)
	}
	gitCLI(t, dir, "fsck", "--strict", "--no-dangling")
	if got := gitCLI(t, dir, "ls-tree", "--name-only", "main"); got != "a-b.md\na.md\na\ndeep" {
		t.Fatalf("tree order = %q", got)
	}
	if got := gitCLI(t, dir, "cat-file", "-p", "main:deep/x/y/z.md"); got != "deep" {
		t.Fatalf("blob = %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != ".git" {
		t.Fatalf("the worktree was touched: %v", entries)
	}

	// Update one file, delete the only file in a directory (the directory disappears),
	// delete a missing path (no-op).
	c2 := commit(t, g, c1, map[string][]byte{
		"a.md": []byte("changed\n"), "deep/x/y/z.md": nil, "missing.md": nil,
	})
	if err := g.SetBranch("main", c1, c2); err != nil {
		t.Fatal(err)
	}
	gitCLI(t, dir, "fsck", "--strict", "--no-dangling")
	if got := gitCLI(t, dir, "ls-tree", "-r", "--name-only", "main"); got != "a-b.md\na.md\na/note.md" {
		t.Fatalf("tree after update = %q", got)
	}
	if got := gitCLI(t, dir, "rev-parse", "main^"); got != c1.String() {
		t.Fatalf("parent = %s, want %s", got, c1)
	}
}

func TestCommitRejects(t *testing.T) {
	g, _ := newRepo(t)
	base := commit(t, g, plumbing.ZeroHash, map[string][]byte{"dir/f.md": []byte("x"), "file.md": []byte("y")})
	tests := []struct {
		path string
		want error
	}{
		{"../escape.md", ErrInvalidPath},
		{"/abs.md", ErrInvalidPath},
		{"a//b.md", ErrInvalidPath},
		{"a/./b.md", ErrInvalidPath},
		{"", ErrInvalidPath},
		{`a\b.md`, ErrInvalidPath},
		{".git/config", ErrInvalidPath},
		{"sub/.GIT/hooks/post-checkout", ErrInvalidPath},
		// Names Windows or macOS treat as .git (#119): git refuses them in a tree too.
		{".git /config", ErrInvalidPath},
		{"sub/GIT~1/hooks/post-checkout", ErrInvalidPath},
		{".git::$INDEX_ALLOCATION/config", ErrInvalidPath},
		{".g" + string(rune(0x200c)) + "it/config", ErrInvalidPath},
		{"dir", ErrTypeConflict},
		{"file.md/child.md", ErrTypeConflict},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			_, err := g.Commit(base, map[string][]byte{tt.path: []byte("z")}, "bad", sig, sig)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSetBranchIsCompareAndSwap(t *testing.T) {
	g, _ := newRepo(t)
	c1 := commit(t, g, plumbing.ZeroHash, map[string][]byte{"a.md": []byte("1")})
	c2 := commit(t, g, c1, map[string][]byte{"a.md": []byte("2")})
	if err := g.SetBranch("main", plumbing.ZeroHash, c1); err != nil {
		t.Fatal(err)
	}
	if err := g.SetBranch("main", plumbing.ZeroHash, c2); !errors.Is(err, ErrRefMoved) {
		t.Fatalf("create over an existing branch: err = %v", err)
	}
	if err := g.SetBranch("main", c2, c1); !errors.Is(err, ErrRefMoved) {
		t.Fatalf("stale old: err = %v", err)
	}
	if err := g.SetBranch("main", c1, c2); err != nil {
		t.Fatal(err)
	}
	if h, _ := g.Branch("main"); h != c2 {
		t.Fatalf("main = %s, want %s", h, c2)
	}
}

func TestRelationAndObjectsToPush(t *testing.T) {
	g, _ := newRepo(t)
	base := commit(t, g, plumbing.ZeroHash, map[string][]byte{"a.md": []byte("base")})
	mine := commit(t, g, base, map[string][]byte{"dir/mine.md": []byte("mine")})
	theirs := commit(t, g, base, map[string][]byte{"theirs.md": []byte("theirs")})
	tests := []struct {
		local, remote Hash
		want          Relation
	}{
		{base, base, Equal},
		{mine, base, Ahead},
		{base, mine, Behind},
		{mine, theirs, Diverged},
	}
	for _, tt := range tests {
		got, err := g.Relation(tt.local, tt.remote)
		if err != nil || got != tt.want {
			t.Errorf("Relation = %v, %v; want %v", got, err, tt.want)
		}
	}
	objs, err := g.ObjectsToPush([]Hash{mine}, []Hash{base})
	if err != nil {
		t.Fatal(err)
	}
	// The commit, its root tree, the new "dir" tree and the new blob; nothing from base.
	if len(objs) != 4 {
		t.Fatalf("ObjectsToPush = %d objects, want 4", len(objs))
	}
}
