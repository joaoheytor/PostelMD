package vault

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"path"
	"strconv"
	"strings"
)

// Save replaces the note at p with content if its current revision is base (If-Match).
// It never creates a note: a missing one is ErrNotFound, so an autosave can't recreate a
// deleted note. A different revision is a *ConflictError carrying the current note, unless
// the note already holds exactly content: then it answers the current revision whatever the
// base, so a retry whose first response was lost succeeds (ADR-0003 §3, §5). Saving
// identical content writes nothing. The new note keeps the old one's permission bits. The
// vault mutex is held from the check until the replacement is durable.
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
	if bytes.Equal(cur, content) {
		return RevOf(cur), nil
	}
	if RevOf(cur) != base {
		return "", &ConflictError{Current: cur, Rev: RevOf(cur)}
	}
	perm, err := v.root.Perm(p)
	if err != nil {
		return "", notFound(err)
	}
	tmp, err := v.writeTemp(content, perm)
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
	if err := checkNewPath(p); err != nil {
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

	tmp, err := v.writeTemp(content, newFilePerm)
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
	if err := checkNewPath(p); err != nil {
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

// Move renames a note or folder. It never replaces the destination (ErrExists), and a
// folder never moves into its own subtree (ErrIntoItself).
func (v *Vault) Move(ctx context.Context, from, to string) error {
	if err := checkPath(from); err != nil {
		return err
	}
	if err := checkNewPath(to); err != nil {
		return err
	}
	if strings.HasPrefix(to, from+"/") {
		return ErrIntoItself
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

// Delete moves a note or folder into the vault's trash and returns its path there,
// "<stamp>/<name>" (ADR-0022): a new folder named after the UTC time of the delete holds the
// item under its own, unchanged name, so any name that fits on disk fits in the trash.
// Nothing is unlinked (CLAUDE.md rule 4); the trash is emptied by the user.
//
// Two steps, mkdir then one RENAME_NOREPLACE. A crash between them leaves an empty stamp
// folder: no data, and the item still in place. The mkdir fails on any existing folder, so
// such a leftover, or another delete in the same instant, is never reused.
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
	folder, err := v.newTrashFolder()
	if err != nil {
		return "", err
	}
	name := folder + "/" + path.Base(p)
	if err := v.root.RenameNoReplace(p, TrashDir+"/"+name); err != nil {
		// Nothing moved: the folder is empty, and only this call made it. RemoveDir removes
		// only an empty folder, so it can never take an item with it.
		return "", errors.Join(existsOrNotFound(err), v.root.RemoveDir(TrashDir+"/"+folder))
	}
	return name, v.syncDirs(TrashDir+"/"+folder, parent(p)) // destination first: see Move
}

// newTrashFolder makes a new, durable folder in the trash, named after the time of the
// delete ("-1", "-2"… after it if one exists), and returns its name.
func (v *Vault) newTrashFolder() (string, error) {
	stamp := v.now().UTC().Format(stampLayout)
	for i := 0; ; i++ {
		folder := stamp
		if i > 0 {
			folder = stamp + "-" + strconv.Itoa(i)
		}
		err := v.root.Mkdir(TrashDir + "/" + folder)
		if errors.Is(err, iofs.ErrExist) && i < 10 {
			continue
		}
		if err != nil {
			return "", existsOrNotFound(err)
		}
		return folder, v.root.SyncDir(TrashDir)
	}
}

func existsOrNotFound(err error) error {
	if errors.Is(err, iofs.ErrExist) {
		return fmt.Errorf("%w: %w", ErrExists, err)
	}
	return notFound(err)
}
