package vault

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// TempHandler is what TakeTemps calls for the entries of the vault's temp folder.
type TempHandler struct {
	// Keep makes a temp's bytes, read from src, durable somewhere else before TakeTemps
	// removes it. It must accept the same name and bytes again: a crash between it and the
	// removal repeats it.
	Keep func(name string, src io.Reader) error
	// Removed runs after a kept temp's unlink, before the folder's fsync.
	Removed func(name string)
	// Skipped gets an entry left in place, with its kind: "folder", "symlink" or "special" (a
	// FIFO, socket or device).
	Skipped func(name, kind string)
}

// TakeTemps is db rebuild's step 3 for the vault at dir (ADR-0030, ADR-0040): every regular
// file in the temp folder apart from the marker, of any size, is opened (no symlink followed,
// O_NONBLOCK, checked with fstat), streamed to h.Keep, then removed; anything else is left
// in place, never opened, and given to h.Skipped. It doesn't open the vault, so nothing else is swept or
// adopted. A vault without the temp folder has nothing to take. The caller holds the
// instance lock (CLAUDE.md rule 12). It returns how many temps were kept.
func TakeTemps(dir string, h TempHandler, opts ...fs.Option) (int, error) {
	root, err := fs.OpenRoot(dir, opts...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	entries, err := root.ReadDir(TmpDir)
	switch {
	case errors.Is(err, iofs.ErrNotExist):
		return 0, nil
	case errors.Is(err, fs.ErrSymlink), errors.Is(err, fs.ErrNotDir):
		return 0, fmt.Errorf("%w: the temp folder (%s) isn't a folder; move it aside, then run this again", ErrBadMarker, TmpDir)
	case err != nil:
		return 0, err
	}
	kept := 0
	for _, e := range entries {
		name := e.Name()
		if name == ownerMarker {
			continue
		}
		if kind := entryKind(e.Type()); kind != "" {
			h.Skipped(name, kind)
			continue
		}
		rel := TmpDir + "/" + name
		f, _, err := root.OpenRegular(rel)
		switch {
		case errors.Is(err, iofs.ErrNotExist):
			continue
		case errors.Is(err, fs.ErrSymlink), errors.Is(err, fs.ErrNotRegular):
			h.Skipped(name, "special") // replaced since the listing
			continue
		case err != nil:
			return kept, fmt.Errorf("vault: read a temp: %w", err)
		}
		err = h.Keep(name, f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return kept, err
		}
		if err := root.Remove(rel); err != nil && !errors.Is(err, iofs.ErrNotExist) {
			return kept, fmt.Errorf("vault: remove a kept temp: %w", err)
		}
		h.Removed(name)
		kept++
	}
	if kept > 0 {
		return kept, root.SyncDir(TmpDir)
	}
	return kept, nil
}

// entryKind is "" for a regular file, else what TakeTemps leaves in place.
func entryKind(m iofs.FileMode) string {
	switch {
	case m.IsRegular():
		return ""
	case m.IsDir():
		return "folder"
	case m&iofs.ModeSymlink != 0:
		return "symlink"
	default:
		return "special"
	}
}

// Inspection is what Inspect found in a vault: how many files /api/v1/files would list, and
// the stat of each asked-for path that is a regular file outside the reserved folders.
type Inspection struct {
	Files int
	Stats map[string]fs.Stat
}

// Inspect counts the vault's files and stats paths, for the identity check (ADR-0040). It
// opens neither the vault (Open adopts and sweeps) nor any file, follows no symlink, and
// changes nothing. The caller holds the instance lock (CLAUDE.md rule 12).
func Inspect(dir string, paths []string, opts ...fs.Option) (Inspection, error) {
	root, err := fs.OpenRoot(dir, opts...)
	if err != nil {
		return Inspection{}, err
	}
	defer func() { _ = root.Close() }()
	in := Inspection{Stats: make(map[string]fs.Stat, len(paths))}
	v := &Vault{root: root}
	if err := v.walk(".", func(string, int64) error { in.Files++; return nil }); err != nil {
		return Inspection{}, err
	}
	for _, p := range paths {
		if fs.IsReserved(p) {
			continue
		}
		st, err := root.Stat(p)
		if err != nil {
			if gone(err) == nil || errors.Is(err, fs.ErrNotDir) {
				continue // not a regular file at that path: no match
			}
			return Inspection{}, err
		}
		in.Stats[p] = st
	}
	return in, nil
}
