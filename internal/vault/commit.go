package vault

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/joaoheytor/PostelMD/internal/store/git"
)

// ErrChangesDiffer: the uncommitted changes aren't the ones the caller listed (a file
// changed, appeared or went back since the status); nothing was committed.
var ErrChangesDiffer = errors.New("vault: the changes differ from the ones listed")

// Listed is one change as the commit dialog listed it: its path and the revision (the save
// protocol's ETag) of the bytes it scanned; nil for a deletion.
type Listed struct {
	Path string `json:"path"`
	Rev  *Rev   `json:"rev"`
}

// Commit commits the uncommitted changes with msg, as author, if they are exactly listed:
// the same paths, each with the same revision (ADR-0020). Otherwise it fails with
// ErrChangesDiffer and commits nothing. The check and the commit hold one acquisition of
// the vault mutex and use one read of the files, so the commit has the bytes checked.
func (v *Vault) Commit(ctx context.Context, msg, name, email string, listed []Listed) (git.Hash, error) {
	return v.commit(ctx, msg, name, email, func(st *Status, _ git.Hash) error { return sameChanges(st.Changes, listed) })
}

// sameChanges reports whether listed names exactly changes: the same paths, a deletion
// listed without a revision, and every other change with the revision of its bytes.
func sameChanges(changes []Change, listed []Listed) error {
	want := make(map[string]*Rev, len(listed))
	for _, l := range listed {
		if _, dup := want[l.Path]; dup {
			return fmt.Errorf("%w: %q listed twice", ErrChangesDiffer, l.Path)
		}
		want[l.Path] = l.Rev
	}
	if len(want) != len(changes) {
		return fmt.Errorf("%w: %d listed, %d changed", ErrChangesDiffer, len(want), len(changes))
	}
	for _, c := range changes {
		rev, ok := want[c.Path]
		same := ok && (c.Kind == Deleted && rev == nil || c.Kind != Deleted && rev != nil && *rev == c.Rev)
		if !same {
			return fmt.Errorf("%w: %q", ErrChangesDiffer, c.Path)
		}
	}
	return nil
}

// commit commits every uncommitted change, after check (if any) accepts the status. It holds
// the vault mutex from reading the files to moving the branch, so the commit has exactly the
// bytes on disk. A crash leaves the branch on the old or the new commit; uncommitted files
// stay uncommitted changes, so the next commit includes them (ADR-0020: no intent needed).
func (v *Vault) commit(ctx context.Context, msg, name, email string, check func(st *Status, head git.Hash) error) (git.Hash, error) {
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
	if check != nil {
		if err := check(st, head); err != nil {
			return git.Hash{}, err
		}
		if v.afterCheck != nil {
			v.afterCheck()
		}
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
