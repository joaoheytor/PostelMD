package vault

import (
	"context"
	"errors"

	"github.com/joaoheytor/PostelMD/internal/store/git"
)

// PendingImport returns how many files the initial import would commit: the uncommitted
// files of a repository without commits, or 0 once it has one (ADR-0020: setup shows the
// count before it commits them).
func (v *Vault) PendingImport(ctx context.Context) (int, error) {
	unlock, err := v.lock(ctx)
	if err != nil {
		return 0, err
	}
	defer unlock()
	st, head, _, err := v.status(false)
	if err != nil || !head.IsZero() {
		return 0, err
	}
	return len(st.Changes), nil
}

// Import is what the initial import committed: how many files, the ones left out for being
// over MaxCommitFile, and the nested repositories left out (ADR-0026).
type Import struct {
	Files        int
	Skipped      []string
	Repositories []string
}

var errHasCommits = errors.New("vault: the repository has commits")

// ImportInitial commits every file of a repository without commits as "Initial import"
// and reports what it committed; a repository with commits is left alone (zero Import).
// The count and the "no commits" check come from the status the commit itself uses, under
// the same acquisition of the vault mutex, so the count is exactly the commit's files.
func (v *Vault) ImportInitial(ctx context.Context, name, email string) (Import, error) {
	var imp Import
	// Unchecked for secrets: the user saw the count at setup, and the notice asks them to
	// check the files before pushing (ADR-0020).
	_, err := v.commit(ctx, InitialImportMessage, name, email, func(st *Status, head git.Hash) error {
		if !head.IsZero() {
			return errHasCommits
		}
		imp = Import{Files: len(st.Changes), Skipped: st.Skipped, Repositories: st.Repositories}
		return nil
	})
	switch {
	case errors.Is(err, errHasCommits):
		return Import{}, nil
	case errors.Is(err, ErrNothingToCommit):
		return Import{Skipped: imp.Skipped, Repositories: imp.Repositories}, nil
	case err != nil:
		return Import{}, err
	}
	return imp, nil
}
