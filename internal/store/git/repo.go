// Package git is the go-git adapter (ADR-0016). go-git only ever touches the vault's .git
// directory: commits are built from bytes the caller supplies, never from the worktree, so
// the bytes the secret scanner checked are exactly the bytes committed (ADR-0010). Worktree
// changes (a pull, for example) are applied by the caller through internal/store/fs under a
// durable intent. Nothing here moves a ref except SetBranch, which is compare-and-swap.
package git

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/go-git/go-billy/v5/osfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
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

// ErrRefMoved: SetBranch found the branch somewhere other than the expected old commit.
var ErrRefMoved = errors.New("store/git: branch moved concurrently")

// Repo is an opened vault repository without a worktree.
type Repo struct {
	r *gogit.Repository
}

// Open opens the repository at vaultDir/.git. go-git gets a filesystem chrooted to .git,
// which refuses symlinks leading out of it. That check runs in user space, so it is not
// race-proof like internal/store/fs; .git is a reserved path no app user can write
// (ADR-0006), which leaves only external writers who already have shell access.
func Open(vaultDir string) (*Repo, error) {
	st := filesystem.NewStorage(osfs.New(filepath.Join(vaultDir, ".git")), cache.NewObjectLRUDefault())
	r, err := gogit.Open(st, nil)
	if err != nil {
		return nil, fmt.Errorf("store/git: open %q: %w", vaultDir, err)
	}
	return &Repo{r: r}, nil
}

// Branch returns the commit a local branch points to.
func (g *Repo) Branch(name string) (Hash, error) {
	ref, err := g.r.Reference(plumbing.NewBranchReferenceName(name), true)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store/git: branch %q: %w", name, err)
	}
	return ref.Hash(), nil
}

// SetBranch moves branch name from old to next atomically, failing with ErrRefMoved if it
// no longer points to old. A zero old means the branch must not exist yet.
func (g *Repo) SetBranch(name string, old, next Hash) error {
	refName := plumbing.NewBranchReferenceName(name)
	var oldRef *plumbing.Reference
	if !old.IsZero() {
		oldRef = plumbing.NewHashReference(refName, old)
	} else if _, err := g.r.Reference(refName, false); err == nil {
		return fmt.Errorf("%w: %s already exists", ErrRefMoved, name)
	}
	err := g.r.Storer.CheckAndSetReference(plumbing.NewHashReference(refName, next), oldRef)
	if errors.Is(err, storage.ErrReferenceHasChanged) {
		return fmt.Errorf("%w: %s", ErrRefMoved, name)
	}
	return err
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
