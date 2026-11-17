package vault

import (
	"context"
	"errors"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// What the startup scan (ADR-0031, internal/ids) needs from the vault: the stat of every
// file, a hash with its stats, one path's stat, and the vault mutex to apply under.

// Stats walks the vault as Survey does (every regular file /api/v1/files would list) and
// returns each file's stat, without hashing. A file that goes away during the walk is left
// out. Any other error fails the whole walk: the scan must never take a file it couldn't
// read for a missing one.
func (v *Vault) Stats(ctx context.Context) (map[string]fs.Stat, error) {
	out := map[string]fs.Stat{}
	err := v.walk(".", func(p string, _ int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		st, ok, err := v.StatPath(p)
		if ok {
			out[p] = st
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Observe hashes p as Survey does: the hash's stat and a stat after it must agree, with
// hashTries tries. ok is false when p isn't a regular file any more; SHA256 is nil when a
// writer kept changing it.
func (v *Vault) Observe(ctx context.Context, p string) (o Observed, ok bool, err error) {
	if err := checkPath(p); err != nil {
		return Observed{}, false, nil
	}
	return v.observe(ctx, p)
}

// StatPath returns p's stat. ok is false when p isn't a regular file /api/v1/files would
// list: missing, a folder, a symlink, reserved or not a valid path.
func (v *Vault) StatPath(p string) (st fs.Stat, ok bool, err error) {
	if err := checkPath(p); err != nil {
		return fs.Stat{}, false, nil
	}
	st, err = v.root.Stat(p)
	if err != nil {
		if errors.Is(notFound(err), ErrNotFound) {
			return fs.Stat{}, false, nil
		}
		return fs.Stat{}, false, err
	}
	return st, true, nil
}

// Locked runs fn under the vault mutex (waiting as long as a save would, then ErrBusy). The
// scan re-checks its paths and commits its pass inside fn: the vault mutex first, then the
// write transaction (ADR-0030). A read-only vault refuses it (ErrReadOnly): fn writes.
func (v *Vault) Locked(ctx context.Context, fn func() error) error {
	unlock, err := v.writeLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}
