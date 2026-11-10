package vault

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"regexp"
	"strings"
	"time"
)

// ErrNotInTrash: the trash id names nothing a delete left there (ADR-0024). It is an
// ErrNotFound, so callers that don't tell them apart answer 404 for both.
var ErrNotInTrash = fmt.Errorf("%w: not in the trash", ErrNotFound)

// ErrNameHasSlash: a restore's new name is a path, not a name; the folder goes separately.
var ErrNameHasSlash = errors.New("vault: a name can't contain /")

// stampLayout is the time format of a trash stamp (ADR-0022).
const stampLayout = "20060102T150405.000000000Z"

var (
	// newEntry is a stamp folder of ADR-0022's layout: <stamp>, or <stamp>-<n> after a
	// second delete in the same instant. It holds the item under its own name.
	newEntry = regexp.MustCompile(`^([0-9]{8}T[0-9]{6}\.[0-9]{9}Z)(-[1-9][0-9]*)?$`)
	// oldEntry is the item itself, renamed <stamp>-<name> before ADR-0022.
	oldEntry = regexp.MustCompile(`^([0-9]{8}T[0-9]{6}\.[0-9]{9}Z)-(.+)$`)
)

// TrashItem is one item in the trash.
type TrashItem struct {
	// ID is the item's entry in the trash folder: what a restore names it by.
	ID        string    `json:"id"`
	Name      string    `json:"name"` // the item's name before the delete
	Kind      Kind      `json:"kind"` // file or folder
	DeletedAt time.Time `json:"deletedAt"`
	Size      *int64    `json:"size,omitempty"` // files only
}

// trashEntry is where a trash entry's item is, once resolved.
type trashEntry struct {
	TrashItem
	src    string // the item's path in the vault
	folder string // the stamp folder that holds it; "" in the old layout
}

// ListTrash returns the trash's items, newest first: at most max, and truncated is true when
// there are more. Both layouts are listed (ADR-0022). Empty stamp folders (a crash's
// leftover), symlinks and other special files, and entries no delete makes are skipped. It
// takes no lock: every change to the trash is an atomic rename or an empty folder.
//
// The whole trash folder is read, and skipped entries don't count towards max: fine for one
// user's trash in the MVP; v0.2's trash record bounds it (ADR-0024). A missing trash, or one
// replaced by a symlink (never followed), lists nothing.
func (v *Vault) ListTrash(max int) (items []TrashItem, truncated bool, err error) {
	entries, err := v.root.ReadDir(TrashDir)
	if err != nil {
		if err = notInTrash(err); errors.Is(err, ErrNotInTrash) {
			return []TrashItem{}, false, nil
		}
		return nil, false, err
	}
	items = []TrashItem{}
	// Stamps are fixed-width UTC times, so the name order is the time order.
	for i := len(entries) - 1; i >= 0; i-- {
		t, err := v.resolveEntry(entries[i])
		if errors.Is(err, ErrNotInTrash) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if len(items) == max {
			return items, true, nil
		}
		items = append(items, t.TrashItem)
	}
	return items, false, nil
}

// findTrashed finds the item of the trash entry id, which a client sent: it is compared
// with the names of the trash's direct children, never opened as a path, and resolveEntry
// then checks its layout.
func (v *Vault) findTrashed(id string) (*trashEntry, error) {
	entries, err := v.root.ReadDir(TrashDir)
	if err != nil {
		return nil, notInTrash(err)
	}
	for _, e := range entries {
		if e.Name() == id {
			return v.resolveEntry(e)
		}
	}
	return nil, ErrNotInTrash
}

// stampOf returns the stamp of a trash entry name in either layout, or "" for a name no
// delete makes.
func stampOf(id string) string {
	m := oldEntry.FindStringSubmatch(id)
	if m == nil {
		m = newEntry.FindStringSubmatch(id)
	}
	if m == nil || checkPath(id) != nil || strings.ContainsRune(id, '/') || len(id) > MaxNameBytes {
		return ""
	}
	return m[1]
}

// resolveEntry finds the item of the trash entry e. Anything that isn't in one of the two
// layouts, holding a file or folder, is ErrNotInTrash.
func (v *Vault) resolveEntry(e iofs.DirEntry) (*trashEntry, error) {
	id := e.Name()
	stamp := stampOf(id)
	at, err := time.Parse(stampLayout, stamp)
	if stamp == "" || err != nil {
		return nil, ErrNotInTrash
	}
	src := TrashDir + "/" + id
	if e.IsDir() && newEntry.MatchString(id) {
		inside, err := v.root.ReadDir(src)
		if err != nil {
			return nil, notInTrash(err)
		}
		switch len(inside) {
		case 0:
			return nil, ErrNotInTrash // a crash's leftover, or a restore's (ADR-0024)
		case 1:
			// A stamp folder: its one entry is the item. An old-layout folder named like a
			// number, holding one entry, reads the same way (ADR-0024, Negative).
			return trashedItem(id, inside[0].Name(), at, inside[0], src+"/"+inside[0].Name(), id)
		}
	}
	// The old layout: the entry is the item itself.
	m := oldEntry.FindStringSubmatch(id)
	if m == nil {
		return nil, ErrNotInTrash
	}
	return trashedItem(id, m[2], at, e, src, "")
}

// notInTrash maps "nothing usable there" (removed meanwhile, a symlink) to ErrNotInTrash.
func notInTrash(err error) error {
	if errors.Is(notFound(err), ErrNotFound) {
		return ErrNotInTrash
	}
	return err
}

// trashedItem describes the item e, named name before the delete, at src.
func trashedItem(id, name string, at time.Time, e iofs.DirEntry, src, folder string) (*trashEntry, error) {
	t := &trashEntry{TrashItem: TrashItem{ID: id, Name: name, DeletedAt: at}, src: src, folder: folder}
	switch {
	case e.IsDir():
		t.Kind = KindFolder
	case e.Type().IsRegular():
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		size := info.Size()
		t.Kind, t.Size = KindFile, &size
	default:
		return nil, ErrNotInTrash // a symlink or special file: never restored
	}
	return t, nil
}

// RestoreTrashed moves the item of trash entry id back into the vault, into folder ("" for
// the root) under name (its own name when ""), and returns its new path. It never replaces
// anything there (ErrExists), and the destination is checked like a move's.
//
// Two steps under the vault mutex (ADR-0024): one RENAME_NOREPLACE of the item out of the
// trash, synced destination first; then, in the new layout, the removal of the emptied
// stamp folder with a remove that only takes an empty directory. A crash between them
// leaves that empty folder, which the listing skips; failing to remove it doesn't undo the
// restore, so it is only logged.
func (v *Vault) RestoreTrashed(ctx context.Context, id, folder, name string) (string, error) {
	unlock, err := v.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	t, err := v.findTrashed(id)
	if err != nil {
		return "", err
	}
	if name == "" {
		name = t.Name
	}
	if strings.ContainsRune(name, '/') {
		return "", ErrNameHasSlash
	}
	to := name
	if folder != "" {
		to = folder + "/" + name
	}
	if err := checkNewPath(to); err != nil {
		return "", err
	}
	if err := v.root.RenameNoReplace(t.src, to); err != nil {
		return "", existsOrNotFound(err)
	}
	if err := v.syncDirs(parent(to), parent(t.src)); err != nil { // destination first: see Move
		return "", err
	}
	if t.folder != "" {
		err := v.root.RemoveDir(TrashDir + "/" + t.folder)
		if err == nil {
			err = v.root.SyncDir(TrashDir)
		}
		if err != nil {
			// The folder name is a stamp, never user content.
			v.log.Warn("trash: couldn't remove an emptied stamp folder; the listing skips it", "err", err.Error())
		}
	}
	return to, nil
}
