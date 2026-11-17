package vault

import (
	"context"
	"errors"
)

// ErrReadOnly: the vault went read-only after a storage fault (ADR-0030, ADR-0037). Reads
// still work; every change is refused until the server restarts.
var ErrReadOnly = errors.New("vault: read-only after a storage error; restart the server once it is fixed")

// SetReadOnly puts the vault read-only for the rest of the process's life: every mutation,
// and the startup scan's commits, then fail with ErrReadOnly. One already holding the vault
// mutex finishes. It reports whether this call made it read-only.
func (v *Vault) SetReadOnly() bool { return v.readOnly.CompareAndSwap(false, true) }

// ReadOnly reports whether SetReadOnly was called (/readyz's state word).
func (v *Vault) ReadOnly() bool { return v.readOnly.Load() }

// writeLock is lock for a mutation: it refuses a read-only vault, before waiting for the
// mutex and again once it holds it (a change that waited while the vault went read-only).
func (v *Vault) writeLock(ctx context.Context) (unlock func(), err error) {
	if v.readOnly.Load() {
		return nil, ErrReadOnly
	}
	unlock, err = v.lock(ctx)
	if err != nil {
		return nil, err
	}
	if v.readOnly.Load() {
		unlock()
		return nil, ErrReadOnly
	}
	return unlock, nil
}
