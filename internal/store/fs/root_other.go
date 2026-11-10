//go:build !linux

package fs

import (
	"errors"
	"fmt"
	iofs "io/fs"
)

// Root is not implemented outside Linux yet. The portable fallback (an O_NOFOLLOW walk
// from the root, ADR-0006) arrives with personal mode (issue #34). Until then the server
// runs on Linux, including inside Docker Desktop's Linux VM.
type Root struct{}

// OpenRoot reports that vault confinement isn't available on this platform.
func OpenRoot(dir string, _ ...Option) (*Root, error) {
	return nil, fmt.Errorf("store/fs: open root %q: %w", dir, errors.ErrUnsupported)
}

// Close is a no-op.
func (r *Root) Close() error { return nil }

// ReadFile is unsupported on this platform.
func (r *Root) ReadFile(string) ([]byte, error) { return nil, errors.ErrUnsupported }

// RenameNoReplace is unsupported on this platform.
func (r *Root) RenameNoReplace(string, string) error { return errors.ErrUnsupported }

// CreateFile is unsupported on this platform.
func (r *Root) CreateFile(string, []byte, uint32) error { return errors.ErrUnsupported }

// Rename is unsupported on this platform.
func (r *Root) Rename(string, string) error { return errors.ErrUnsupported }

// Mkdir is unsupported on this platform.
func (r *Root) Mkdir(string) error { return errors.ErrUnsupported }

// SyncDir is unsupported on this platform.
func (r *Root) SyncDir(string) error { return errors.ErrUnsupported }

// Remove is unsupported on this platform.
func (r *Root) Remove(string) error { return errors.ErrUnsupported }

// SyncFile is unsupported on this platform.
func (r *Root) SyncFile(string) error { return errors.ErrUnsupported }

// ReadDir is unsupported on this platform.
func (r *Root) ReadDir(string) ([]iofs.DirEntry, error) { return nil, errors.ErrUnsupported }
