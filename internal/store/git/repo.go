// Package git is the go-git adapter (ADR-0016). go-git only ever touches the vault's .git
// directory: commits are built from bytes the caller supplies, never from the worktree, so
// the bytes the secret scanner checked are exactly the bytes committed (ADR-0010). Worktree
// changes (a pull, for example) are applied by the caller through internal/store/fs under a
// durable intent. Nothing here moves a ref except SetBranch, which is compare-and-swap and
// writes the ref itself: go-git rewrites refs in place without fsync (ADR-0016,
// "Durability").
package git

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// Hash is a git object ID.
type Hash = plumbing.Hash

// Signature is a commit author or committer.
type Signature = object.Signature

// Relation of a local branch to its remote-tracking branch.
type Relation int

// Possible relations; see Repo.Relation.
const (
	Equal Relation = iota
	Ahead
	Behind
	Diverged
)

func (r Relation) String() string {
	return [...]string{"equal", "ahead", "behind", "diverged"}[r]
}

var (
	// ErrRefMoved: SetBranch found the branch somewhere other than the expected old commit.
	ErrRefMoved = errors.New("store/git: branch moved concurrently")
	// ErrRefLocked: the branch's lock file exists. Another git process holds it, or a crash
	// left it behind; only the recovery of the intent that names it may remove it (ADR-0016).
	ErrRefLocked = errors.New("store/git: branch is locked")
)

// Repo is an opened vault repository without a worktree.
type Repo struct {
	r      *gogit.Repository
	dotgit *fs.Root // the same .git directory, for writing refs durably
}

// Open opens the repository at vaultDir/.git. go-git gets a filesystem chrooted to .git,
// which refuses symlinks leading out of it. That check runs in user space, so it is not
// race-proof like internal/store/fs; .git is a reserved path no app user can write
// (ADR-0006), which leaves only external writers who already have shell access.
//
// opts configure the root that SetBranch writes refs through (fs.WithHook in tests).
func Open(vaultDir string, opts ...fs.Option) (*Repo, error) {
	dir := filepath.Join(vaultDir, ".git")
	st := filesystem.NewStorage(osfs.New(dir), cache.NewObjectLRUDefault())
	r, err := gogit.Open(st, nil)
	if err != nil {
		return nil, fmt.Errorf("store/git: open %q: %w", vaultDir, err)
	}
	dotgit, err := fs.OpenRoot(dir, opts...)
	if err != nil {
		return nil, fmt.Errorf("store/git: open %q: %w", vaultDir, err)
	}
	return &Repo{r: r, dotgit: dotgit}, nil
}

// Close releases the repository's directory handle.
func (g *Repo) Close() error { return g.dotgit.Close() }

// Branch returns the commit a local branch points to.
func (g *Repo) Branch(name string) (Hash, error) {
	ref, err := g.r.Reference(plumbing.NewBranchReferenceName(name), true)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store/git: branch %q: %w", name, err)
	}
	return ref.Hash(), nil
}

// SetBranch moves branch name from old to next atomically, failing with ErrRefMoved if it
// no longer points to old. A zero old means the branch must not exist yet. It uses git's
// own lockfile protocol, so it also excludes a concurrent external git:
//
//  1. create refs/heads/<name>.lock exclusively with the new value, and fsync it
//     (ErrRefLocked if the lock exists);
//  2. compare the branch's current value (loose or packed) with old; on a mismatch, remove
//     the lock and return ErrRefMoved;
//  3. rename the lock over the ref and fsync the directory.
//
// The caller's intent names the lock (ADR-0016), so a crash at any step is recoverable: the
// ref is always the old or the new value, never empty or torn.
func (g *Repo) SetBranch(name string, old, next Hash) error {
	refName := plumbing.NewBranchReferenceName(name)
	if err := refName.Validate(); err != nil {
		return fmt.Errorf("store/git: branch %q: %w", name, err)
	}
	ref := refName.String()
	lock := ref + ".lock"
	if err := g.dotgit.CreateFile(lock, []byte(next.String()+"\n"), 0o644); err != nil {
		if errors.Is(err, iofs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrRefLocked, lock)
		}
		return fmt.Errorf("store/git: lock %s: %w", ref, err)
	}
	if err := g.checkBranch(refName, old); err != nil {
		if rmErr := g.dotgit.Remove(lock); rmErr != nil {
			return errors.Join(err, fmt.Errorf("store/git: unlock %s: %w", ref, rmErr))
		}
		return err
	}
	if err := g.dotgit.Rename(lock, ref); err != nil {
		return fmt.Errorf("store/git: set %s: %w", ref, err)
	}
	dir, _ := path.Split(ref)
	if err := g.dotgit.SyncDir(strings.TrimSuffix(dir, "/")); err != nil {
		return fmt.Errorf("store/git: sync %s: %w", ref, err)
	}
	return nil
}

// checkBranch verifies that refName points to old (or doesn't exist, for a zero old).
func (g *Repo) checkBranch(refName plumbing.ReferenceName, old Hash) error {
	cur, err := g.r.Reference(refName, false)
	switch {
	case errors.Is(err, plumbing.ErrReferenceNotFound):
		if old.IsZero() {
			return nil
		}
		return fmt.Errorf("%w: %s does not exist", ErrRefMoved, refName)
	case err != nil:
		return fmt.Errorf("store/git: read %s: %w", refName, err)
	case cur.Hash() != old:
		return fmt.Errorf("%w: %s is at %s", ErrRefMoved, refName, cur.Hash())
	}
	return nil
}

// Relation compares local with remote. Callers check it before pushing: go-git reports a
// rejected non-fast-forward push only as an unexported error string.
func (g *Repo) Relation(local, remote Hash) (Relation, error) {
	if local == remote {
		return Equal, nil
	}
	lc, err := g.r.CommitObject(local)
	if err != nil {
		return 0, fmt.Errorf("store/git: local commit: %w", err)
	}
	rc, err := g.r.CommitObject(remote)
	if err != nil {
		return 0, fmt.Errorf("store/git: remote commit: %w", err)
	}
	if behind, err := lc.IsAncestor(rc); err != nil || behind {
		return Behind, err
	}
	if ahead, err := rc.IsAncestor(lc); err != nil || ahead {
		return Ahead, err
	}
	return Diverged, nil
}

// ObjectsToPush lists every object reachable from want but not from have: exactly what a
// push sends, and so what the push gate must scan (ADR-0010).
func (g *Repo) ObjectsToPush(want, have []Hash) ([]Hash, error) {
	objs, err := revlist.Objects(g.r.Storer, want, have)
	if err != nil {
		return nil, fmt.Errorf("store/git: reachable objects: %w", err)
	}
	return objs, nil
}
