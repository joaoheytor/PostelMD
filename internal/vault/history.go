package vault

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/git"
)

// Git types the API returns, so callers above the vault never import the git store.
type (
	Hash     = git.Hash
	Version  = git.Version
	LockInfo = git.LockInfo
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
	// ErrDetached: the repository's HEAD isn't on a branch; the user fixes it on the host.
	ErrDetached = errors.New("vault: git HEAD is detached; check out a branch on the host")
)

// InitialImportMessage is the message of the commit setup makes for an adopted folder.
const InitialImportMessage = "Initial import"

// ChangeKind of an uncommitted change.
type ChangeKind string

const (
	Added    ChangeKind = "added"
	Modified ChangeKind = "modified"
	Deleted  ChangeKind = "deleted"
)

// Change is one uncommitted file. Rev is the revision of the bytes read (the ETag a read of
// the note answers); a deletion has none. The commit dialog scans the bytes whose ETag is
// Rev, and commits by Rev (see Commit).
type Change struct {
	Path string     `json:"path"`
	Kind ChangeKind `json:"kind"`
	Rev  Rev        `json:"rev,omitempty"`
}

// Status is the working tree compared with the branch.
type Status struct {
	Branch  string   `json:"branch"`
	Changes []Change `json:"changes"`
	Skipped []string `json:"skipped,omitempty"` // over MaxCommitFile: never committed
	// Repositories are folders with a .git of their own (a clone or a submodule): never
	// descended into, as git add -A records a gitlink there; an existing gitlink is kept.
	Repositories []string `json:"repositories,omitempty"`
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
	// An adopted repository may lack the reserved folders in info/exclude. Best effort: a
	// read-only or symlinked one only means the user's own git may list the trash.
	if err := repo.EnsureExclude(); err != nil {
		v.log.Warn("couldn't add PostelMD's folders to .git/info/exclude", "err", err)
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
	if errors.Is(err, git.ErrDetached) {
		return nil, head, nil, fmt.Errorf("%w: %w", ErrDetached, err)
	}
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
	var repos []string // nested repositories, with a trailing slash
	err = v.walkForCommit(committed, func(p string, size int64, ignored bool) error {
		old, tracked := committed[p]
		if ignored && !tracked {
			return nil // ignored and untracked: not a change; a tracked file stays tracked
		}
		seen[p] = true
		if size > MaxCommitFile {
			st.Skipped = append(st.Skipped, p)
			return nil
		}
		b, err := v.root.ReadFile(p)
		if err != nil {
			return err
		}
		kind := Modified
		switch {
		case !tracked:
			kind = Added
		case old == git.BlobHash(b):
			return nil
		}
		st.Changes = append(st.Changes, Change{Path: p, Kind: kind, Rev: RevOf(b)})
		if load {
			content[p] = b
		}
		return nil
	}, func(p string, ignored bool) {
		repos = append(repos, p+"/")
		if !ignored {
			st.Repositories = append(st.Repositories, p)
		}
	})
	if err != nil {
		return nil, head, nil, err
	}
	for p := range committed {
		// Committed notes in a folder that has since become a repository are left as they
		// are, never listed as deleted: a commit would drop them from the history (ADR-0026).
		inRepo := slices.ContainsFunc(repos, func(r string) bool { return strings.HasPrefix(p, r) })
		if !seen[p] && !inRepo && !fs.IsReserved(p) {
			st.Changes = append(st.Changes, Change{Path: p, Kind: Deleted})
			content[p] = nil
		}
	}
	sort.Slice(st.Changes, func(i, j int) bool { return st.Changes[i].Path < st.Changes[j].Path })
	return st, head, content, nil
}

// ParseCommit parses a full hex commit ID; anything else is ErrNotFound.
func ParseCommit(s string) (git.Hash, error) {
	if len(s) != 40 || strings.Trim(strings.ToLower(s), "0123456789abcdef") != "" {
		return git.Hash{}, fmt.Errorf("%w: commit %q", ErrNotFound, s)
	}
	return git.NewHash(strings.ToLower(s)), nil
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

// Version returns note p's content in commit (a full hex ID).
func (v *Vault) Version(p, commit string) ([]byte, error) {
	if err := checkPath(p); err != nil {
		return nil, err
	}
	if v.git == nil {
		return nil, ErrNoGit
	}
	h, err := ParseCommit(commit)
	if err != nil {
		return nil, err
	}
	b, err := v.git.File(h, p, MaxNoteSize)
	if errors.Is(err, git.ErrNotInCommit) {
		return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return b, err
}

// Restore writes note p's content from commit through the save path (ADR-0020): it
// replaces the note only if it is still at base, and never recreates a deleted note.
func (v *Vault) Restore(ctx context.Context, p, commit string, base Rev) (Rev, error) {
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
