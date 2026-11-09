package vault

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"path"
	"strconv"
)

// Save replaces the note at p with content if its current revision is base (If-Match).
// It never creates a note: a missing one is ErrNotFound, so an autosave can't recreate a
// deleted note. A different revision is a *ConflictError carrying the current note.
// Saving identical content writes nothing. The vault mutex is held from the check until
// the replacement is durable.
func (v *Vault) Save(ctx context.Context, p string, base Rev, content []byte) (Rev, error) {
	if err := checkPath(p); err != nil {
		return "", err
	}
	if len(content) > MaxNoteSize {
		return "", ErrTooLarge
	}
	unlock, err := v.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()

	cur, err := v.root.ReadFile(p)
	if err != nil {
		return "", notFound(err)
	}
	if RevOf(cur) != base {
		return "", &ConflictError{Current: cur, Rev: RevOf(cur)}
	}
	if bytes.Equal(cur, content) {
		return base, nil
	}
	tmp, err := v.writeTemp(content)
	if err != nil {
		return "", err
	}
	if err := v.root.Rename(tmp, p); err != nil {
		return "", errors.Join(err, v.root.Remove(tmp))
	}
	if err := v.syncDirs(parent(p), TmpDir); err != nil {
		return "", err
	}
	return RevOf(content), nil
}

// Create writes a new note at p (If-None-Match: *). It fails with ErrExists if anything is
// already there, and with ErrNotFound if the folder doesn't exist.
func (v *Vault) Create(ctx context.Context, p string, content []byte) (Rev, error) {
	if err := checkPath(p); err != nil {
		return "", err
	}
	if len(content) > MaxNoteSize {
		return "", ErrTooLarge
	}
	unlock, err := v.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()

	tmp, err := v.writeTemp(content)
	if err != nil {
		return "", err
	}
	if err := v.root.RenameNoReplace(tmp, p); err != nil {
		if rmErr := v.root.Remove(tmp); rmErr != nil {
			return "", errors.Join(err, rmErr)
		}
		return "", existsOrNotFound(err)
	}
	if err := v.syncDirs(parent(p), TmpDir); err != nil {
		return "", err
	}
	return RevOf(content), nil
}

// Mkdir creates the folder p; its parent must exist.
func (v *Vault) Mkdir(ctx context.Context, p string) error {
	if err := checkPath(p); err != nil {
		return err
	}
	unlock, err := v.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := v.root.Mkdir(p); err != nil {
		return existsOrNotFound(err)
	}
	return v.root.SyncDir(parent(p))
}

// Move renames a note or folder. It never replaces the destination (ErrExists).
func (v *Vault) Move(ctx context.Context, from, to string) error {
	if err := checkPath(from); err != nil {
		return err
	}
	if err := checkPath(to); err != nil {
		return err
	}
	unlock, err := v.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := v.root.RenameNoReplace(from, to); err != nil {
		return existsOrNotFound(err)
	}
	// Destination first. A power loss between the two syncs can then leave the item in
	// both places (a duplicate to clean up), never in neither (a loss).
	return v.syncDirs(parent(to), parent(from))
}

// Delete moves a note or folder into the vault's trash and returns its name there. Nothing
// is unlinked (CLAUDE.md rule 4); the trash is emptied by the user.
func (v *Vault) Delete(ctx context.Context, p string) (string, error) {
	if err := checkPath(p); err != nil {
		return "", err
	}
	unlock, err := v.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	// The trash must be durable before anything moves into it, or a power loss could drop
	// the trash directory together with the item.
	if err := v.ensureDir(TrashDir); err != nil {
		return "", err
	}
	stamp := v.now().UTC().Format("20060102T150405.000000000Z")
	for i := 0; ; i++ {
		name := stamp + "-" + path.Base(p)
		if i > 0 {
			name = stamp + "-" + strconv.Itoa(i) + "-" + path.Base(p)
		}
		err := v.root.RenameNoReplace(p, TrashDir+"/"+name)
		if errors.Is(err, iofs.ErrExist) && i < 3 {
			continue
		}
		if err != nil {
			return "", existsOrNotFound(err)
		}
		return name, v.syncDirs(TrashDir, parent(p)) // destination first: see Move
	}
}

func existsOrNotFound(err error) error {
	if errors.Is(err, iofs.ErrExist) {
		return fmt.Errorf("%w: %w", ErrExists, err)
	}
	return notFound(err)
}
