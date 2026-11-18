package vault

// What intent recovery (ADR-0033) needs from the vault: each side of an intent judged on its
// hash and inode, the temps read and removed by their op ID, and the sweep, which the start
// runs after recovery instead of at Open.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"strings"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// Judged is one side of an intent as recovery sees it: nothing there, something that isn't
// a regular file (a folder, a symlink, a device), or a regular file with the stat taken
// through the descriptor it was hashed from, and its full hash.
type Judged struct {
	Present bool // something is at the path
	Regular bool // a regular file: Stat and SHA256 are set
	Stat    fs.Stat
	SHA256  [sha256.Size]byte
}

// Judge judges the vault path p. A reserved or invalid path is ErrNotFound: recovery judges
// only the paths a client could name.
func (v *Vault) Judge(ctx context.Context, p string) (Judged, error) {
	if err := checkPath(p); err != nil {
		return Judged{}, err
	}
	return v.judge(ctx, p)
}

// JudgeTemp judges the temp named name, directly under the temp folder.
func (v *Vault) JudgeTemp(ctx context.Context, name string) (Judged, error) {
	if err := checkTemp(name); err != nil {
		return Judged{}, err
	}
	return v.judge(ctx, TmpDir+"/"+name)
}

func (v *Vault) judge(ctx context.Context, rel string) (Judged, error) {
	st, sum, err := v.root.Hash(ctx, rel)
	switch {
	case err == nil:
		return Judged{Present: true, Regular: true, Stat: st, SHA256: sum}, nil
	case errors.Is(err, iofs.ErrNotExist), errors.Is(err, fs.ErrNotDir):
		return Judged{}, nil // a parent that isn't a folder holds nothing at rel either
	case errors.Is(err, fs.ErrSymlink), errors.Is(err, fs.ErrNotRegular):
		return Judged{Present: true}, nil
	}
	return Judged{}, err
}

// OpenNote opens the regular file p for reading, without following a symlink: recovery
// copies a side it keeps.
func (v *Vault) OpenNote(p string) (io.ReadCloser, error) {
	if err := checkPath(p); err != nil {
		return nil, err
	}
	f, _, err := v.root.OpenRegular(p)
	if err != nil {
		return nil, notFound(err)
	}
	return f, nil
}

// OpenTemp opens the temp named name for reading, as OpenNote.
func (v *Vault) OpenTemp(name string) (io.ReadCloser, error) {
	if err := checkTemp(name); err != nil {
		return nil, err
	}
	f, _, err := v.root.OpenRegular(TmpDir + "/" + name)
	if err != nil {
		return nil, notFound(err)
	}
	return f, nil
}

// RemoveTemp unlinks the temp named name and syncs the temp folder; a temp already gone is
// done. It also ends a power loss's second name for a renamed temp (ADR-0033's "same inode"
// rows): unlinking that name leaves the file under its other one.
func (v *Vault) RemoveTemp(name string) error {
	if err := checkTemp(name); err != nil {
		return err
	}
	err := v.root.Remove(TmpDir + "/" + name)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return v.root.SyncDir(TmpDir)
}

// SyncSaved makes a save's rename onto p durable: p's folder, then the temp folder (ADR-0033's
// save step 6). Recovery runs it before rolling a save forward.
func (v *Vault) SyncSaved(p string) error {
	if err := checkPath(p); err != nil {
		return err
	}
	return v.syncDirs(parent(p), TmpDir)
}

// checkTemp accepts a plain name in the temp folder, never the marker.
func checkTemp(name string) error {
	if name == "" || name == "." || name == ".." || name == ownerMarker || strings.ContainsRune(name, '/') {
		return fmt.Errorf("%w: not a temp's name", ErrNotFound)
	}
	return nil
}

// Sweep removes every entry of the temp folder but the marker and those spare names (nil
// spares none): a save that didn't finish never answered 200, so its edit is still the
// client's draft. The start runs it after intent recovery, sparing the temps the remaining
// (flagged) intents name (ADR-0033, superseding ADR-0027's sweep at Open). The caller holds
// the instance lock and serves nothing yet.
func (v *Vault) Sweep(spare func(name string) bool) error {
	entries, err := v.root.ReadDir(TmpDir)
	if err != nil {
		return err
	}
	swept := 0
	for _, e := range entries {
		if e.Name() == ownerMarker || (spare != nil && spare(e.Name())) {
			continue
		}
		swept++
		rm := v.root.Remove
		if e.IsDir() { // a repository Init didn't finish
			rm = v.removeTree
		}
		if err := rm(TmpDir + "/" + e.Name()); err != nil {
			return fmt.Errorf("vault: sweep %s: %w", e.Name(), err)
		}
	}
	if swept > 0 {
		return v.root.SyncDir(TmpDir)
	}
	return nil
}
