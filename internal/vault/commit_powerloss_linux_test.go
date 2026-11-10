//go:build linux

package vault

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
)

// powerCut is faultfs's power-loss model (ADR-0003 §5) for .git, where go-git writes loose
// objects through its own billy filesystem: only the syncs (syncFS) and the index and ref
// lockfiles reach the hook, so faultfs.Disk, which replays every write, can't model them.
// powerCut models the same rule from the syncs alone, by inode: a file's data survives as
// it was at its last fsync (empty if never synced), and a directory's entries as they were
// at its last fsync. Everything in .git when the cut is armed counts as durable. It crashes
// like faultfs.Disk: step crashAt (0-based) and every later one fail with ErrCrashed.
type powerCut struct {
	gitDir         string
	armed, crashed bool
	crashAt, steps int
	root           uint64
	isDir          map[uint64]bool
	entries        map[uint64]map[string]uint64 // directory -> entries that survive
	synced         map[uint64][]byte            // file -> content that survives
	bad            error                        // a step the model can't place in .git
}

func inode(info iofs.FileInfo) uint64 { return info.Sys().(*syscall.Stat_t).Ino }

// arm snapshots .git as fully durable; steps count from here.
func (c *powerCut) arm(t *testing.T, gitDir string) {
	t.Helper()
	c.gitDir, c.armed = gitDir, true
	c.isDir, c.entries, c.synced = map[uint64]bool{}, map[uint64]map[string]uint64{}, map[uint64][]byte{}
	fsys := os.DirFS(gitDir)
	err := iofs.WalkDir(fsys, ".", func(p string, d iofs.DirEntry, err error) error {
		var info iofs.FileInfo
		if err == nil {
			info, err = d.Info()
		}
		switch {
		case err != nil:
			return err
		case p == ".":
			c.root = inode(info)
		}
		if d.IsDir() {
			return c.syncDir(filepath.Join(gitDir, p), inode(info))
		}
		c.synced[inode(info)], err = iofs.ReadFile(fsys, p)
		return err
	})
	must(t, err)
}

func (c *powerCut) syncDir(p string, ino uint64) error {
	list, err := os.ReadDir(p)
	if err != nil {
		return err
	}
	c.isDir[ino] = true
	c.entries[ino] = map[string]uint64{}
	for _, e := range list {
		info, err := e.Info()
		if err != nil {
			return err
		}
		c.entries[ino][e.Name()], c.isDir[inode(info)] = inode(info), e.IsDir()
	}
	return nil
}

func (c *powerCut) Before(fs.Op) error {
	switch {
	case !c.armed:
		return nil
	case c.crashed:
		return faultfs.ErrCrashed
	case c.steps == c.crashAt:
		c.crashed = true
		c.steps++
		return faultfs.ErrCrashed
	}
	c.steps++
	return nil
}

func (c *powerCut) After(op fs.Op, err error) {
	if !c.armed || err != nil || c.bad != nil {
		return
	}
	if op.Kind == fs.OpRename || op.Kind == fs.OpRenameNoReplace || op.Kind == fs.OpRemove {
		return // entries change only when their directory is synced
	}
	p := filepath.Join(c.gitDir, op.Path)
	info, statErr := os.Lstat(p)
	if statErr != nil { // the commit must only touch .git
		c.bad = fmt.Errorf("step %v %s outside .git: %w", op.Kind, op.Path, statErr)
		return
	}
	switch ino := inode(info); op.Kind {
	case fs.OpCreate: // a new file: an inode reused from a removed one starts unsynced
		delete(c.synced, ino)
	case fs.OpMkdir:
		c.isDir[ino], c.entries[ino] = true, map[string]uint64{}
	case fs.OpSyncFile:
		c.synced[ino], c.bad = os.ReadFile(p)
	case fs.OpSyncDir:
		c.bad = c.syncDir(p, ino)
	}
}

// restore writes what .git would hold after a power loss now into dst.
func (c *powerCut) restore(t *testing.T, dst string) {
	t.Helper()
	var write func(ino uint64, dir string)
	write = func(ino uint64, dir string) {
		must(t, os.MkdirAll(dir, 0o700))
		for name, child := range c.entries[ino] {
			if c.isDir[child] {
				write(child, filepath.Join(dir, name))
				continue
			}
			must(t, os.WriteFile(filepath.Join(dir, name), c.synced[child], 0o600))
		}
	}
	write(c.root, dst)
}

func writeFiles(t *testing.T, dir string, f map[string]string) {
	t.Helper()
	for p, b := range f {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o700))
		must(t, os.WriteFile(filepath.Join(dir, p), []byte(b), 0o600))
	}
}

// TestCommitPowerLossEverywhere cuts the power at every step of a commit (ADR-0016,
// "Durability"; ADR-0020): after a restart, the branch is at the old or the new commit and
// reaches only complete objects (git fsck --strict), each change is in the new commit or
// still an uncommitted change with its bytes intact, and the next commit succeeds, after
// "Remove the lock" if a lock was left behind.
func TestCommitPowerLossEverywhere(t *testing.T) {
	want := map[string]string{"a.md": "A2", "notes/new/b.md": "B", "notes/x.md": "X"}
	cases := []struct {
		name  string
		first bool     // the repository has no commit yet
		edits []Change // the uncommitted changes the commit takes
	}{
		{"first commit", true, []Change{{Path: "a.md", Kind: Added}, {Path: "notes/new/b.md", Kind: Added}, {Path: "notes/x.md", Kind: Added}}},
		{"next commit", false, []Change{{Path: "a.md", Kind: Modified}, {Path: "c.md", Kind: Deleted}, {Path: "notes/new/b.md", Kind: Added}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for crashAt := 0; ; crashAt++ {
				if crashAt > 100 {
					t.Fatal("the commit never completed in 100 steps")
				}
				completed := false
				if !t.Run(fmt.Sprintf("step %d", crashAt), func(t *testing.T) {
					completed = powerLossCommit(t, crashAt, tc.first, tc.edits, want)
				}) {
					t.FailNow() // the first broken crash point is the one to read
				}
				if completed {
					return
				}
			}
		})
	}
}

// powerLossCommit runs one crash point and reports whether the commit completed.
func powerLossCommit(t *testing.T, crashAt int, first bool, edits []Change, want map[string]string) bool {
	t.Helper()
	dir := t.TempDir()
	if first {
		writeFiles(t, dir, want)
	} else {
		writeFiles(t, dir, map[string]string{"a.md": "A", "c.md": "C", "notes/x.md": "X"})
	}
	cut := &powerCut{crashAt: crashAt}
	v, err := Open(dir, fs.WithHook(cut))
	must(t, err)
	_, err = v.EnableGit()
	must(t, err)
	old := ""
	if !first {
		c, err := commitAll(v, "first", "Ada", "a@b")
		must(t, err)
		old = c.String()
		writeFiles(t, dir, want)
		must(t, os.Remove(filepath.Join(dir, "c.md")))
	}
	cut.arm(t, filepath.Join(dir, ".git"))
	_, err = commitAll(v, "second", "Ada", "a@b")
	_ = v.Close()
	if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
		t.Fatalf("%v", err)
	}
	if cut.bad != nil {
		t.Fatalf("%v", cut.bad)
	}
	completed := !cut.crashed

	// The disk after the power comes back. A commit never writes the working tree, so it is
	// as durable as before; the temp directory is left out, as a restart sweeps it.
	after := t.TempDir()
	writeFiles(t, after, worktree(files(t, dir)))
	cut.restore(t, filepath.Join(after, ".git"))
	gitCLI(t, after, "fsck", "--strict", "--no-dangling")

	ref, err := os.ReadFile(filepath.Join(after, ".git", "refs", "heads", "main"))
	head := strings.TrimSpace(string(ref))
	switch {
	case errors.Is(err, os.ErrNotExist) && first && !completed:
		head = "" // still unborn: the old state of a first commit
	case err != nil:
		t.Fatalf("branch unreadable: %v", err)
	}
	moved := head != old
	if moved {
		if got := gitCLI(t, after, "log", "-1", "--format=%P", head); got != old {
			t.Fatalf("main = %s, whose parent is %q, want the old commit %q or its child", head, got, old)
		}
		checkTree(t, after, head, want)
	} else if completed {
		t.Fatalf("the commit returned but main is still at the old commit")
	}

	v, err = Open(after)
	must(t, err)
	defer func() { _ = v.Close() }()
	_, err = v.EnableGit()
	must(t, err)
	got := changes(t, v)
	if moved && len(got) != 0 {
		t.Fatalf("main moved, yet changes are left: %v", got)
	}
	if !moved && !reflect.DeepEqual(got, edits) {
		t.Fatalf("main at the old commit, changes = %v, want %v", got, edits)
	}
	if f := files(t, after); !reflect.DeepEqual(worktree(f), want) {
		t.Fatalf("working tree = %v, want %v", worktree(f), want)
	}
	if !moved {
		_, err = commitAll(v, "retry", "Ada", "a@b")
		if errors.Is(err, ErrGitLocked) { // the user confirms no other git is running
			must(t, v.RemoveGitLocks(ctx))
			_, err = commitAll(v, "retry", "Ada", "a@b")
		}
		must(t, err)
		gitCLI(t, after, "fsck", "--strict", "--no-dangling")
		checkTree(t, after, "HEAD", want)
	}
	if got := gitCLI(t, after, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("the host's git sees %q: the index doesn't match HEAD", got)
	}
	return completed
}

// worktree drops git's own files from a listing of the vault.
func worktree(f map[string]string) map[string]string {
	out := map[string]string{}
	for p, b := range f {
		if !strings.HasPrefix(p, ".git/") {
			out[p] = b
		}
	}
	return out
}

// checkTree asserts that commit holds exactly want.
func checkTree(t *testing.T, dir, commit string, want map[string]string) {
	t.Helper()
	paths := make([]string, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if got := gitCLI(t, dir, "ls-tree", "-r", "--name-only", commit); got != strings.Join(paths, "\n") {
		t.Fatalf("%s holds %q, want %q", commit, got, paths)
	}
	for _, p := range paths {
		if got := gitCLI(t, dir, "show", commit+":"+p); got != want[p] {
			t.Fatalf("%s:%s = %q, want %q", commit, p, got, want[p])
		}
	}
}
