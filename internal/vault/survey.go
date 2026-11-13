package vault

import (
	"context"
	"crypto/sha256"
	"errors"
	iofs "io/fs"

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
		o, ok, err := v.observe(p)
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

// observe hashes p between two stats that agree with the hash's own (same file, unchanged).
// ok is false for a file that went away.
func (v *Vault) observe(p string) (o Observed, ok bool, err error) {
	o.Path = p
	for range hashTries {
		before, err := v.root.Stat(p)
		if err != nil {
			return o, false, gone(err)
		}
		during, sum, err := v.root.Hash(p)
		if err != nil {
			return o, false, gone(err)
		}
		if v.afterHash != nil {
			v.afterHash(p)
		}
		if o.Stat, err = v.root.Stat(p); err != nil {
			return o, false, gone(err)
		}
		if before == during && during == o.Stat {
			o.SHA256 = &sum
			return o, true, nil
		}
	}
	return o, true, nil
}

// gone is nil for an error that means the file isn't a regular file at its path any more.
func gone(err error) error {
	if errors.Is(err, iofs.ErrNotExist) || errors.Is(err, fs.ErrSymlink) || errors.Is(err, fs.ErrNotRegular) {
		return nil
	}
	return err
}
