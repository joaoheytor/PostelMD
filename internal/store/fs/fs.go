// Package fs is the only package that touches vault files (CLAUDE.md rule 1). It confines
// every operation to a vault root and never follows symlinks (ADR-0006).
//
// os.Root is not enough on its own: it follows symlinks that stay inside the root, and
// Root.Rename silently replaces an existing destination (both verified in the v0.0
// confinement spike). So on Linux every path is resolved with openat2(RESOLVE_BENEATH |
// RESOLVE_NO_SYMLINKS) relative to the root's directory fd, and moves use
// renameat2(RENAME_NOREPLACE).
package fs

import (
	"errors"
	"path/filepath"
	"strings"
)

var (
	// ErrInvalidPath: the path is empty, absolute, not clean, or leaves the root ("..").
	ErrInvalidPath = errors.New("store/fs: invalid vault path")
	// ErrSymlink: a component of the path is a symlink. Symlinks are never followed.
	ErrSymlink = errors.New("store/fs: path contains a symlink")
	// ErrEscape: the kernel refused a resolution that would leave the root.
	ErrEscape = errors.New("store/fs: path escapes the vault root")
	// ErrNotRegular: the path is not a regular file (a directory, device, FIFO...).
	ErrNotRegular = errors.New("store/fs: not a regular file")
	// ErrNotDir: a component used as a directory is not one.
	ErrNotDir = errors.New("store/fs: not a directory")
	// ErrNoReplaceUnsupported: the filesystem can't rename without replacing (for example
	// 9p, used by WSL and Docker Desktop bind mounts). Callers use the two-step fallback
	// from ADR-0003 §2 under a structural intent.
	ErrNoReplaceUnsupported = errors.New("store/fs: filesystem does not support RENAME_NOREPLACE")
)

// MaxFileSize bounds ReadFile (ADR-0003 §8 limits are enforced above this layer; this is a
// safety net against reading a huge file into memory).
const MaxFileSize = 64 << 20

// checkRel accepts only clean, relative, slash-separated paths that stay inside the root.
func checkRel(p string) error {
	if p == "" || p == "." || strings.ContainsRune(p, 0) || strings.Contains(p, `\`) {
		return ErrInvalidPath
	}
	if !filepath.IsLocal(p) || filepath.Clean(p) != p {
		return ErrInvalidPath
	}
	return nil
}

// splitParent returns the parent directory ("." for the root) and the final element.
func splitParent(p string) (dir, base string) {
	dir, base = filepath.Split(p)
	if dir == "" {
		return ".", base
	}
	return strings.TrimSuffix(dir, "/"), base
}
