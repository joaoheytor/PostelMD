//go:build !linux

package lock

import (
	"errors"
	"fmt"
)

// HeldError: another process holds the lock.
type HeldError struct{ Holder string }

func (e *HeldError) Error() string { return "lock: held by " + e.Holder }

// Lock is unsupported outside Linux, like the vault store (issue #34).
type Lock struct{}

// Acquire reports that the instance lock isn't available on this platform.
func Acquire(path string) (*Lock, error) {
	return nil, fmt.Errorf("lock: %s: %w", path, errors.ErrUnsupported)
}

// Release is a no-op.
func (l *Lock) Release() error { return nil }
