//go:build linux

package vault

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/git"
)

// gitCLI runs the real git on dir: the repository PostelMD writes must stay a plain git
// repository the user's own tools read (ADR-0020).
func gitCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null") // the developer's own git config stays out
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ident := []string{"-c", "user.name=Host", "-c", "user.email=host@example.invalid", "-C", dir}
	out, err := exec.Command("git", append(ident, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func changes(t *testing.T, v *Vault) []Change {
	t.Helper()
	st, err := v.Status(ctx)
	must(t, err)
	return st.Changes
}

func TestAdoptCommitAndHistory(t *testing.T) {
	v, dir := newVault(t)
	must(t, os.WriteFile(filepath.Join(dir, "b.md"), []byte("B"), 0o600))
	created, err := v.EnableGit()
	if err != nil || !created {
		t.Fatalf("EnableGit = %v, %v; want a new repository", created, err)
	}
	if _, err := v.Delete(ctx, "b.md"); err != nil { // a trashed note is never committed
		t.Fatal(err)
	}
	want := []Change{{"notes/a.md", Added}}
	if got := changes(t, v); !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %v, want %v", got, want)
	}
	first, err := v.Commit(ctx, "  Initial import  ", "Ada", "ada@example.invalid")
	must(t, err)
	if got := gitCLI(t, dir, "ls-files"); got != "notes/a.md" {
		t.Fatalf("committed files = %q", got)
	}
	if got := gitCLI(t, dir, "log", "--format=%an <%ae> %s"); got != "Ada <ada@example.invalid> Initial import" {
		t.Fatalf("git log = %q", got)
	}
	gitCLI(t, dir, "fsck", "--strict")
	if got := gitCLI(t, dir, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("git sees changes after a commit: %q", got)
	}
	if _, err := v.Commit(ctx, "again", "Ada", "a@b"); !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("empty commit = %v", err)
	}

	rev, err := v.Save(ctx, "notes/a.md", RevOf([]byte("A")), []byte("A2"))
	must(t, err)
	_, err = v.Create(ctx, "c.md", []byte("C"))
	must(t, err)
	want = []Change{{"c.md", Added}, {"notes/a.md", Modified}}
	if got := changes(t, v); !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %v, want %v", got, want)
	}
	_, err = v.Commit(ctx, "Edit", "Ada", "ada@example.invalid")
	must(t, err)
	_, err = v.Delete(ctx, "c.md")
	must(t, err)
	if got := changes(t, v); !reflect.DeepEqual(got, []Change{{"c.md", Deleted}}) {
		t.Fatalf("status after a delete = %v", got)
	}
	_, err = v.Commit(ctx, "Remove c", "Ada", "ada@example.invalid")
	must(t, err)

	hist, err := v.History("notes/a.md", 10)
	must(t, err)
	if len(hist) != 2 || hist[0].Message != "Edit" || hist[1].Commit != first {
		t.Fatalf("history = %+v", hist)
	}
	if h, _ := v.History("c.md", 10); len(h) != 2 || !h[0].Deleted || h[1].Deleted {
		t.Fatalf("history of a deleted note = %+v", h)
	}
	if b, err := v.Version("notes/a.md", first); err != nil || string(b) != "A" {
		t.Fatalf("old version = %q, %v", b, err)
	}
	if _, err := v.Restore(ctx, "notes/a.md", first, RevOf([]byte("stale"))); !errors.Is(err, ErrConflict) {
		t.Fatalf("restore over a newer edit = %v, want ErrConflict", err)
	}
	if _, err := v.Restore(ctx, "notes/a.md", first, rev); err != nil || content(t, dir, "notes/a.md") != "A" {
		t.Fatalf("restore = %v", err)
	}
	if _, err := v.Restore(ctx, "c.md", first, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restoring a note absent from that commit = %v", err)
	}
	if _, err := v.History(".git/config", 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("history of a reserved path = %v", err)
	}
}

func TestAdoptAnExistingRepository(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("A"), 0o600))
	gitCLI(t, dir, "init", "-q", "-b", "trunk")
	gitCLI(t, dir, "add", "a.md")
	gitCLI(t, dir, "commit", "-q", "-m", "from the host")
	v, err := Open(dir)
	must(t, err)
	defer func() { _ = v.Close() }()
	if created, err := v.EnableGit(); err != nil || created {
		t.Fatalf("EnableGit = %v, %v; want the existing repository", created, err)
	}
	st, err := v.Status(ctx)
	must(t, err)
	if st.Branch != "trunk" || len(st.Changes) != 0 {
		t.Fatalf("status = %+v", st)
	}
	must(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("A2"), 0o600))
	_, err = v.Commit(ctx, "from PostelMD", "Ada", "ada@example.invalid")
	must(t, err)
	if got := gitCLI(t, dir, "log", "--format=%s", "trunk"); got != "from PostelMD\nfrom the host" {
		t.Fatalf("git log = %q", got)
	}

	gitCLI(t, dir, "checkout", "-q", "--detach")
	if _, err := v.Commit(ctx, "x", "Ada", "a@b"); !errors.Is(err, git.ErrDetached) {
		t.Fatalf("commit on a detached HEAD = %v", err)
	}
}

func TestGitLockNeedsTheUser(t *testing.T) {
	v, dir := newVault(t)
	_, err := v.EnableGit()
	must(t, err)
	lock := filepath.Join(dir, ".git", "refs", "heads", "main.lock")
	must(t, os.WriteFile(lock, []byte("left by a crash\n"), 0o600))
	if _, err := v.Commit(ctx, "x", "Ada", "a@b"); !errors.Is(err, ErrGitLocked) {
		t.Fatalf("commit with a lock = %v, want ErrGitLocked", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("a failed commit removed the lock by itself")
	}
	locks, err := v.GitLocks()
	if err != nil || len(locks) != 1 || locks[0].Path != ".git/refs/heads/main.lock" {
		t.Fatalf("GitLocks = %+v, %v", locks, err)
	}
	if _, err := v.Save(ctx, "notes/a.md", RevOf([]byte("A")), []byte("A2")); err != nil {
		t.Fatalf("saving notes must keep working while commits are blocked: %v", err)
	}
	must(t, os.WriteFile(filepath.Join(dir, ".git", "index.lock"), nil, 0o600))
	if locks, _ := v.GitLocks(); len(locks) != 2 {
		t.Fatalf("GitLocks = %+v, want the index lock too", locks)
	}
	must(t, v.RemoveGitLocks(ctx))
	if locks, _ := v.GitLocks(); len(locks) != 0 {
		t.Fatalf("locks left: %+v", locks)
	}
	if _, err := v.Commit(ctx, "after the lock", "Ada", "a@b"); err != nil {
		t.Fatal(err)
	}
}

// failAt fails the n-th store step (counting from 0), like a process killed there.
type failAt struct{ n, step int }

var errKilled = errors.New("killed")

func (f *failAt) Before(fs.Op) error {
	f.step++
	if f.step-1 == f.n {
		return errKilled
	}
	return nil
}
func (f *failAt) After(fs.Op, error) {}

// TestCommitCrashEverywhere kills a commit at every store step. The branch is always the
// old or the new commit; when it is the old one, the files are still uncommitted changes and
// the next commit includes them (ADR-0020). Init is killed the same way: a restart sweeps
// the half-built repository, and the next EnableGit creates a complete one.
func TestCommitCrashEverywhere(t *testing.T) {
	for n := 0; ; n++ {
		dir := t.TempDir()
		must(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("A"), 0o600))
		hook := &failAt{n: -1}
		v, err := Open(dir, fs.WithHook(hook))
		must(t, err)
		hook.n, hook.step = n, 0
		_, err = v.EnableGit()
		if err == nil {
			_, err = v.Commit(ctx, "first", "Ada", "a@b")
		}
		_ = v.Close()
		killed := hook.step > n
		if err != nil && !errors.Is(err, errKilled) {
			t.Fatalf("kill at %d: %v", n, err)
		}

		v, err = Open(dir) // restart
		must(t, err)
		_, err = v.EnableGit()
		must(t, err)
		if entries, _ := os.ReadDir(filepath.Join(dir, TmpDir)); len(entries) != 0 {
			t.Fatalf("kill at %d: %d leftovers in the temp directory", n, len(entries))
		}
		st, err := v.Status(ctx)
		must(t, err)
		// Either the commit landed (nothing to commit) or the file is still a change.
		if !killed && len(st.Changes) != 0 {
			t.Fatalf("an uninterrupted commit left changes: %v", st.Changes)
		}
		if len(st.Changes) != 0 {
			_, err = v.Commit(ctx, "retry", "Ada", "a@b")
			if errors.Is(err, ErrGitLocked) { // the user confirms no other git is running
				must(t, v.RemoveGitLocks(ctx))
				_, err = v.Commit(ctx, "retry", "Ada", "a@b")
			}
			must(t, err)
		}
		_ = v.Close()
		gitCLI(t, dir, "fsck", "--strict")
		if got := gitCLI(t, dir, "show", "HEAD:a.md"); got != "A" {
			t.Fatalf("kill at %d: HEAD:a.md = %q", n, got)
		}
		if got := gitCLI(t, dir, "status", "--porcelain"); got != "" {
			t.Fatalf("kill at %d: the host's git sees %q", n, got)
		}
		if !killed {
			return
		}
	}
}
