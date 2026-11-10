package vault

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/git"
)

// MaxCommitFile is the largest file a commit includes; larger ones are listed as skipped.
const MaxCommitFile = 50 << 20

var (
	ErrNoGit           = errors.New("vault: git isn't enabled")
	ErrNothingToCommit = errors.New("vault: nothing to commit")
	ErrBadMessage      = errors.New("vault: the commit message must be 1 to 10000 characters, not only spaces")
	// ErrGitLocked: a leftover or live branch lock blocks commits (ADR-0020); see GitLock.
	ErrGitLocked = errors.New("vault: a git lock is blocking commits")
	// ErrGitMoved: something outside PostelMD committed meanwhile; retry.
	ErrGitMoved = errors.New("vault: the branch moved during the commit, retry")
)

// ChangeKind of an uncommitted change.
type ChangeKind string

const (
	Added    ChangeKind = "added"
	Modified ChangeKind = "modified"
	Deleted  ChangeKind = "deleted"
)

// Change is one uncommitted file.
type Change struct {
	Path string     `json:"path"`
	Kind ChangeKind `json:"kind"`
}

// Status is the working tree compared with the branch.
type Status struct {
	Branch  string   `json:"branch"`
	Changes []Change `json:"changes"`
	Skipped []string `json:"skipped,omitempty"` // over MaxCommitFile: never committed
}

// EnableGit opens the vault's repository, creating an empty one first if there is none
// (ADR-0020: adoption keeps an existing repository as it is). It reports whether it
// created one.
func (v *Vault) EnableGit() (bool, error) {
	if err := v.ensureDir(TmpDir); err != nil {
		return false, err
	}
	created, err := git.Init(v.root, TmpDir)
	if err != nil {
		return false, err
	}
	repo, err := git.Open(v.dir, v.opts...)
	if err != nil {
		return created, err
	}
	v.git = repo
	return created, nil
}

// Status lists the uncommitted changes. It takes the vault mutex, so it never sees a
// half-finished move.
func (v *Vault) Status(ctx context.Context) (*Status, error) {
	unlock, err := v.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	st, _, _, err := v.status(false)
	return st, err
}

// status compares the working tree with the branch head. With load, it also returns the
// changed files' content (nil for a deletion), read under the caller's mutex.
func (v *Vault) status(load bool) (*Status, git.Hash, map[string][]byte, error) {
	if v.git == nil {
		return nil, git.Hash{}, nil, ErrNoGit
	}
	branch, head, err := v.git.Head()
	if err != nil {
		return nil, head, nil, err
	}
	committed, err := v.git.Files(head)
	if err != nil {
		return nil, head, nil, err
	}
	st := &Status{Branch: branch, Changes: []Change{}}
	content := map[string][]byte{}
	seen := map[string]bool{}
	err = v.walk(".", func(p string, size int64) error {
		seen[p] = true
		if size > MaxCommitFile {
			st.Skipped = append(st.Skipped, p)
			return nil
		}
		b, err := v.root.ReadFile(p)
		if err != nil {
			return err
		}
		old, tracked := committed[p]
		switch {
		case !tracked:
			st.Changes = append(st.Changes, Change{p, Added})
		case old != git.BlobHash(b):
			st.Changes = append(st.Changes, Change{p, Modified})
		default:
			return nil
		}
		if load {
			content[p] = b
		}
		return nil
	})
	if err != nil {
		return nil, head, nil, err
	}
	for p := range committed {
		if !seen[p] && !fs.IsReserved(p) {
			st.Changes = append(st.Changes, Change{p, Deleted})
			content[p] = nil
		}
	}
	sort.Slice(st.Changes, func(i, j int) bool { return st.Changes[i].Path < st.Changes[j].Path })
	return st, head, content, nil
}

// walk calls fn for every regular file under dir that isn't reserved. Symlinks and other
// special files are skipped, never followed.
func (v *Vault) walk(dir string, fn func(p string, size int64) error) error {
	entries, err := v.root.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := e.Name()
		if dir != "." {
			p = dir + "/" + p
		}
		if fs.IsReserved(p) {
			continue
		}
		switch {
		case e.IsDir():
			if err := v.walk(p, fn); err != nil {
				return err
			}
		case e.Type().IsRegular():
			info, err := e.Info()
			if err != nil {
				return err
			}
			if err := fn(p, info.Size()); err != nil {
				return err
			}
		}
	}
	return nil
}

// Commit commits every uncommitted change with msg, as author. It holds the vault mutex from
// reading the files to moving the branch, so the commit has exactly the bytes on disk. A
// crash leaves the branch on the old or the new commit; uncommitted files stay uncommitted
// changes, so the next commit includes them (ADR-0020: no intent needed).
func (v *Vault) Commit(ctx context.Context, msg, name, email string) (git.Hash, error) {
	msg = strings.TrimSpace(msg)
	if msg == "" || len(msg) > 10000 || strings.ContainsRune(msg, 0) {
		return git.Hash{}, ErrBadMessage
	}
	unlock, err := v.lock(ctx)
	if err != nil {
		return git.Hash{}, err
	}
	defer unlock()
	st, head, changes, err := v.status(true)
	if err != nil {
		return git.Hash{}, err
	}
	if len(changes) == 0 {
		return git.Hash{}, ErrNothingToCommit
	}
	if locks, err := v.git.Locks(st.Branch); err != nil || len(locks) > 0 {
		return git.Hash{}, errors.Join(err, fmt.Errorf("%w: %s", ErrGitLocked, locks))
	}
	sig := git.Signature{Name: name, Email: email, When: v.now()}
	c, err := v.git.Commit(head, changes, msg+"\n", sig, sig)
	if err != nil {
		return git.Hash{}, err
	}
	// The index first: if the branch then doesn't move, the user's git sees the changes as
	// staged, which is true; the reverse order would show them as reverted.
	if err := v.git.ResetIndex(c); err != nil {
		return git.Hash{}, lockErr(err)
	}
	switch err := v.git.SetBranch(st.Branch, head, c); {
	case errors.Is(err, git.ErrRefLocked):
		return git.Hash{}, lockErr(err)
	case errors.Is(err, git.ErrRefMoved):
		return git.Hash{}, fmt.Errorf("%w: %w", ErrGitMoved, err)
	case err != nil:
		return git.Hash{}, err
	}
	return c, nil
}

func lockErr(err error) error {
	if errors.Is(err, git.ErrRefLocked) || errors.Is(err, git.ErrIndexLocked) {
		return fmt.Errorf("%w: %w", ErrGitLocked, err)
	}
	return err
}

// History lists the commits that changed note p, newest first.
func (v *Vault) History(p string, limit int) ([]git.Version, error) {
	if err := checkPath(p); err != nil {
		return nil, err
	}
	if v.git == nil {
		return nil, ErrNoGit
	}
	_, head, err := v.git.Head()
	if err != nil {
		return nil, err
	}
	return v.git.Log(head, p, limit)
}

// Version returns note p's content in commit.
func (v *Vault) Version(p string, commit git.Hash) ([]byte, error) {
	if err := checkPath(p); err != nil {
		return nil, err
	}
	if v.git == nil {
		return nil, ErrNoGit
	}
	b, err := v.git.File(commit, p, MaxNoteSize)
	if errors.Is(err, git.ErrNotInCommit) {
		return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return b, err
}

// Restore writes note p's content from commit through the save path (ADR-0020): it
// replaces the note only if it is still at base, and never recreates a deleted note.
func (v *Vault) Restore(ctx context.Context, p string, commit git.Hash, base Rev) (Rev, error) {
	b, err := v.Version(p, commit)
	if err != nil {
		return "", err
	}
	return v.Save(ctx, p, base, b)
}

// GitLocks returns the git locks blocking commits on the current branch (ADR-0020).
func (v *Vault) GitLocks() ([]git.LockInfo, error) {
	if v.git == nil {
		return nil, ErrNoGit
	}
	branch, _, err := v.git.Head()
	if err != nil {
		return nil, err
	}
	return v.git.Locks(branch)
}

// RemoveGitLocks removes those locks after the user confirmed that no other git command is
// running (ADR-0020). Nothing is lost: the branch is on the old or the new commit, the
// index is only a cache, and anything not committed is still in the working tree.
func (v *Vault) RemoveGitLocks(ctx context.Context) error {
	if v.git == nil {
		return ErrNoGit
	}
	unlock, err := v.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	branch, _, err := v.git.Head()
	if err != nil {
		return err
	}
	return v.git.RemoveLocks(branch)
}

// removeTree removes the directory p and everything in it (app-owned temp directories
// only). Symlinks inside are unlinked, never followed.
func (v *Vault) removeTree(p string) error {
	entries, err := v.root.ReadDir(p)
	if err != nil {
		return err
	}
	for _, e := range entries {
		child := path.Join(p, e.Name())
		if e.IsDir() {
			err = v.removeTree(child)
		} else {
			err = v.root.Remove(child)
		}
		if err != nil {
			return err
		}
	}
	return v.root.RemoveDir(p)
}
