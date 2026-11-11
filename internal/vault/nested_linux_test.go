//go:build linux

package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// nestedRepos adds, under dir, a cloned repository (clone/, with a .git folder) and a
// submodule-style checkout (sub/, with a .git file), each holding a note of its own.
func nestedRepos(t *testing.T, dir string) {
	t.Helper()
	writeFiles(t, dir, map[string]string{"clone/inner.md": "clone's note", "sub/inner.md": "submodule's note",
		"sub/.git": "gitdir: ../.git/modules/sub\n"})
	gitCLI(t, filepath.Join(dir, "clone"), "init", "-q")
	gitCLI(t, filepath.Join(dir, "clone"), "add", "-A")
	gitCLI(t, filepath.Join(dir, "clone"), "commit", "-q", "-m", "clone")
}

// TestImportSkipsNestedRepositories: the initial import doesn't descend into a folder with
// its own .git (a folder or a file): git add -A would record a gitlink there, not the inner
// repository's files. The import names those folders instead.
func TestImportSkipsNestedRepositories(t *testing.T) {
	v, dir := newVault(t)
	nestedRepos(t, dir)
	_, err := v.EnableGit()
	must(t, err)
	imp, err := v.ImportInitial(ctx, "Ada", "ada@example.invalid")
	must(t, err)
	committed := gitCLI(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	if committed != "notes/a.md" || imp.Files != 1 || !reflect.DeepEqual(imp.Repositories, []string{"clone", "sub"}) {
		t.Fatalf("ImportInitial = %+v, committed %q; want notes/a.md only, clone and sub named", imp, committed)
	}
	st, err := v.Status(ctx)
	must(t, err)
	if len(st.Changes) != 0 || !reflect.DeepEqual(st.Repositories, []string{"clone", "sub"}) {
		t.Fatalf("Status = %+v, want no changes and the two repositories named", st)
	}
}

// TestCommitKeepsAGitlink: when the vault's repository already records a nested repository
// as a gitlink (a submodule, or a clone added on the host), commits leave it as it is, and
// don't fail on its folder ("file/directory conflict: clone is a file").
func TestCommitKeepsAGitlink(t *testing.T) {
	v, dir := newVault(t)
	nestedRepos(t, dir)
	_, err := v.EnableGit()
	must(t, err)
	gitCLI(t, dir, "add", "notes/a.md", "clone") // clone becomes a gitlink, as on the host
	gitCLI(t, dir, "commit", "-q", "-m", "host")
	gitlink := gitCLI(t, dir, "ls-tree", "HEAD", "clone")
	if !strings.HasPrefix(gitlink, "160000 commit ") {
		t.Fatalf("fixture: clone is %q, want a gitlink", gitlink)
	}
	must(t, os.WriteFile(filepath.Join(dir, "notes", "a.md"), []byte("A2"), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "clone", "inner.md"), []byte("changed inside the clone"), 0o600))
	if got := paths(changes(t, v)); !reflect.DeepEqual(got, []string{"notes/a.md"}) {
		t.Fatalf("changes = %v, want notes/a.md only", got)
	}
	if _, err := commitAll(v, "edit", "Ada", "ada@example.invalid"); err != nil {
		t.Fatalf("commit next to a gitlink: %v", err)
	}
	if got := gitCLI(t, dir, "ls-tree", "HEAD", "clone"); got != gitlink {
		t.Fatalf("clone after the commit = %q, want the gitlink %q", got, gitlink)
	}
	if got := gitCLI(t, dir, "status", "--porcelain", "--", "notes"); got != "" {
		t.Fatalf("git status after the commit: %q", got)
	}
}
