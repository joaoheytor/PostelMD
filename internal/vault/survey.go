package vault

import (
	"context"
	"crypto/sha256"
	"errors"
	iofs "io/fs"
	"slices"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// hashTries is how many times the first assignment hashes a file a writer keeps changing
// before it records the file without a hash (ADR-0031).
const hashTries = 3

// Observed is one regular file the survey found.
type Observed struct {
	Path   string
	Stat   fs.Stat
	SHA256 *[sha256.Size]byte // nil: a writer was busy for every try; the scan hashes it
}

// Survey walks the vault for the first assignment (ADR-0031): every regular file
// /api/v1/files would list (reserved paths and symlinks excluded). For each it stats, hashes
// and stats again, and hashes again if the two stats differ; after hashTries it records the
// file without a hash. A file removed, or replaced by something that isn't a regular file,
// during the walk is left out: the scan sees what is there later. It takes no lock and
// changes nothing.
func (v *Vault) Survey(ctx context.Context) ([]Observed, error) {
	var out []Observed
	err := v.walk(".", func(p string, _ int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		o, ok, err := v.observe(ctx, p)
		if ok {
			out = append(out, o)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// observe hashes p and stats it again: the hash's own stat, taken through its descriptor
// before reading, is ADR-0031's first stat, so a file replaced at its path (another inode)
// or changed while read (size, mtime, ctime) shows as two stats that differ. ok is false for
// a file that went away.
func (v *Vault) observe(ctx context.Context, p string) (o Observed, ok bool, err error) {
	o.Path = p
	for range hashTries {
		during, sum, err := v.root.Hash(ctx, p)
		if err != nil {
			return o, false, gone(err)
		}
		if v.afterHash != nil {
			v.afterHash(p)
		}
		if o.Stat, err = v.root.Stat(p); err != nil {
			return o, false, gone(err)
		}
		if during == o.Stat {
			o.SHA256 = &sum
			return o, true, nil
		}
	}
	return o, true, nil
}

// TrashForImport returns every item in the trash, oldest first, for ADR-0031's trash import
// at the first assignment. It reads the trash as ListTrash does (ADR-0024): a stamp folder
// holding one entry is that entry (ADR-0022); a <stamp>-<name> entry is the old layout; empty
// stamp folders, symlinks, special files and names no delete makes are skipped. A missing
// trash holds nothing. It takes no lock and changes nothing.
func (v *Vault) TrashForImport() ([]TrashItem, error) {
	items, _, err := v.ListTrash(-1) // newest first; -1 is never reached, so no limit
	if err != nil {
		return nil, err
	}
	slices.Reverse(items)
	return items, nil
}

// gone is nil for an error that means the file isn't a regular file at its path any more.
func gone(err error) error {
	if errors.Is(err, iofs.ErrNotExist) || errors.Is(err, fs.ErrSymlink) || errors.Is(err, fs.ErrNotRegular) {
		return nil
	}
	return err
}
