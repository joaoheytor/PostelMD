//go:build !linux

package fs

import (
	"errors"
	"fmt"
)

// Root is not implemented outside Linux yet. The portable fallback (an O_NOFOLLOW walk
// from the root, ADR-0006) arrives with personal mode (issue #34). Until then the server
// runs on Linux, including inside Docker Desktop's Linux VM.
type Root struct{}

// OpenRoot reports that vault confinement isn't available on this platform.
func OpenRoot(dir string) (*Root, error) {
	return nil, fmt.Errorf("store/fs: open root %q: %w", dir, errors.ErrUnsupported)
}

// Close is a no-op.
func (r *Root) Close() error { return nil }

// ReadFile is unsupported on this platform.
func (r *Root) ReadFile(string) ([]byte, error) { return nil, errors.ErrUnsupported }

// RenameNoReplace is unsupported on this platform.
func (r *Root) RenameNoReplace(string, string) error { return errors.ErrUnsupported }
