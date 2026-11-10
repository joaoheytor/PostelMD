package git

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

var (
	// ErrDetached: HEAD points to a commit, not a branch, so there's no branch to commit to.
	ErrDetached = errors.New("store/git: HEAD is detached; check out a branch on the host")
	// ErrNotInCommit: the path is not a file in that commit.
	ErrNotInCommit = errors.New("store/git: no such file in that commit")
)

// NewHash parses a hex object ID.
func NewHash(s string) Hash { return plumbing.NewHash(s) }

// BlobHash is the git object ID of a file with content data.
func BlobHash(data []byte) Hash { return plumbing.ComputeHash(plumbing.BlobObject, data) }

// Head returns the branch HEAD points to and its commit, which is zero on an unborn branch
// (a new repository).
func (g *Repo) Head() (branch string, commit Hash, err error) {
	ref, err := g.r.Storer.Reference(plumbing.HEAD)
	if err != nil {
		return "", plumbing.ZeroHash, fmt.Errorf("store/git: HEAD: %w", err)
	}
	if ref.Type() != plumbing.SymbolicReference || !ref.Target().IsBranch() {
		return "", plumbing.ZeroHash, ErrDetached
	}
	branch = ref.Target().Short()
	c, err := g.Branch(branch)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return branch, plumbing.ZeroHash, nil
	}
	return branch, c, err
}

// Files lists the regular files in commit's tree, path -> blob ID. Symlinks and submodules
// are left out: commits never touch them. A zero commit has no files.
func (g *Repo) Files(commit Hash) (map[string]Hash, error) {
	out := map[string]Hash{}
	if commit.IsZero() {
		return out, nil
	}
	c, err := g.r.CommitObject(commit)
	if err != nil {
		return nil, fmt.Errorf("store/git: commit: %w", err)
	}
	t, err := c.Tree()
	if err != nil {
		return nil, fmt.Errorf("store/git: tree: %w", err)
	}
	err = t.Files().ForEach(func(f *object.File) error {
		if f.Mode == filemode.Regular || f.Mode == filemode.Executable {
			out[f.Name] = f.Hash
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store/git: files: %w", err)
	}
	return out, nil
}

// Version is one commit that changed a file.
type Version struct {
	Commit  string    `json:"commit"` // hex
	Message string    `json:"message"`
	Author  string    `json:"author"`
	When    time.Time `json:"when"`
	Deleted bool      `json:"deleted"` // the commit removed the file
}

// Log lists the commits reachable from head that changed path, newest first, at most limit.
func (g *Repo) Log(head Hash, path string, limit int) ([]Version, error) {
	if head.IsZero() {
		return nil, nil
	}
	if err := checkPath(path); err != nil {
		return nil, fmt.Errorf("%w: %q", err, path)
	}
	iter, err := g.r.Log(&gogit.LogOptions{From: head, FileName: &path})
	if err != nil {
		return nil, fmt.Errorf("store/git: log: %w", err)
	}
	defer iter.Close()
	var out []Version
	err = iter.ForEach(func(c *object.Commit) error {
		_, ferr := c.File(path)
		out = append(out, Version{
			Commit: c.Hash.String(), Message: strings.TrimSpace(c.Message), Author: c.Author.Name,
			When: c.Author.When, Deleted: errors.Is(ferr, object.ErrFileNotFound),
		})
		if len(out) >= limit {
			return storer.ErrStop
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store/git: log: %w", err)
	}
	return out, nil
}

// File returns path's content in commit, reading at most max bytes (more is an error).
func (g *Repo) File(commit Hash, path string, max int64) ([]byte, error) {
	if err := checkPath(path); err != nil {
		return nil, fmt.Errorf("%w: %q", err, path)
	}
	c, err := g.r.CommitObject(commit)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotInCommit, err)
	}
	f, err := c.File(path)
	if err != nil || (f.Mode != filemode.Regular && f.Mode != filemode.Executable) {
		return nil, fmt.Errorf("%w: %q", ErrNotInCommit, path)
	}
	if f.Size > max {
		return nil, fmt.Errorf("store/git: %q is %d bytes, over %d", path, f.Size, max)
	}
	r, err := f.Reader()
	if err != nil {
		return nil, fmt.Errorf("store/git: read %q: %w", path, err)
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(io.LimitReader(r, max))
}

// LockInfo describes a git lock file.
type LockInfo struct {
	Path     string    `json:"path"` // relative to the vault
	Modified time.Time `json:"modified"`
}

// Locks returns the lock files that block a commit on branch: the index lock and the
// branch's ref lock. Empty means none.
func (g *Repo) Locks(branch string) ([]LockInfo, error) {
	var out []LockInfo
	for _, lock := range []string{indexLock, plumbing.NewBranchReferenceName(branch).String() + ".lock"} {
		dir, base := ".", lock
		if i := strings.LastIndex(lock, "/"); i >= 0 {
			dir, base = lock[:i], lock[i+1:]
		}
		entries, err := g.dotgit.ReadDir(dir)
		if errors.Is(err, iofs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("store/git: read %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.Name() != base {
				continue
			}
			info, err := e.Info()
			if err != nil {
				return nil, fmt.Errorf("store/git: stat %s: %w", lock, err)
			}
			out = append(out, LockInfo{Path: ".git/" + lock, Modified: info.ModTime()})
		}
	}
	return out, nil
}

// RemoveLocks removes the locks Locks reports, durably. Only call it once the user has
// confirmed that no other git command is running (ADR-0020): one signal, a file's
// existence, can't tell a crash's leftover from a live git process.
func (g *Repo) RemoveLocks(branch string) error {
	locks, err := g.Locks(branch)
	if err != nil {
		return err
	}
	for _, l := range locks {
		rel := strings.TrimPrefix(l.Path, ".git/")
		if err := g.dotgit.Remove(rel); err != nil {
			return fmt.Errorf("store/git: remove %s: %w", rel, err)
		}
		dir := "."
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			dir = rel[:i]
		}
		if err := g.dotgit.SyncDir(dir); err != nil {
			return err
		}
	}
	return nil
}
