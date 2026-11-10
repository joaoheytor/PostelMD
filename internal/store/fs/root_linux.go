//go:build linux

package fs

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// resolveFlags: stay beneath the root and refuse every symlink and magic link.
const resolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS

// Root is an open vault directory. All operations are relative to its directory fd, so
// renaming or replacing the vault's own path after opening doesn't redirect them.
type Root struct {
	dir  *os.File
	hook Hook
}

// OpenRoot opens dir as a vault root. dir comes from configuration, not from users.
func OpenRoot(dir string, opts ...Option) (*Root, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	fd, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("store/fs: open root %q: %w", dir, err)
	}
	if err := probeOpenat2(fd); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &Root{dir: os.NewFile(uintptr(fd), dir), hook: o.hook}, nil
}

// Close releases the root's directory fd.
func (r *Root) Close() error { return r.dir.Close() }

// probeOpenat2 fails fast on kernels without openat2 (< 5.6) instead of on first use.
func probeOpenat2(dirfd int) error {
	fd, err := unix.Openat2(dirfd, ".", &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: resolveFlags,
	})
	if err != nil {
		return fmt.Errorf("store/fs: openat2 unavailable (Linux >= 5.6 required): %w", err)
	}
	return unix.Close(fd)
}

// openat resolves rel under the root. RESOLVE_NO_SYMLINKS rejects a symlink in any
// component, including the last one. Don't add O_NOFOLLOW: combined with O_PATH it makes
// the kernel return the symlink itself instead of failing.
func (r *Root) openat(rel string, flags uint64) (int, error) {
	fd, err := unix.Openat2(int(r.dir.Fd()), rel, &unix.OpenHow{
		Flags:   flags | unix.O_CLOEXEC,
		Resolve: resolveFlags,
	})
	if err != nil {
		return -1, mapErr("open", rel, err)
	}
	return fd, nil
}

// ReadFile returns the contents of the regular file at rel.
func (r *Root) ReadFile(rel string) ([]byte, error) {
	if err := checkRel(rel); err != nil {
		return nil, err
	}
	fd, err := r.openat(rel, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), rel)
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, mapErr("stat", rel, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("%w: %s", ErrNotRegular, rel)
	}
	if st.Size > MaxFileSize {
		return nil, fmt.Errorf("store/fs: %s is larger than %d bytes", rel, MaxFileSize)
	}
	return io.ReadAll(io.LimitReader(f, MaxFileSize+1))
}

// ReadDir lists the directory rel ("." for the root), sorted by name. Entry types come from
// the directory itself, so a symlink is listed as a symlink and never followed. Each
// entry's Info is read relative to that directory (fstatat, no follow) while it is open:
// os.File.ReadDir's own Info would lstat the name relative to the working directory.
func (r *Root) ReadDir(rel string) ([]iofs.DirEntry, error) {
	if rel != "." {
		if err := checkRel(rel); err != nil {
			return nil, err
		}
	}
	fd, err := r.openat(rel, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), rel)
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("store/fs: list %s: %w", rel, err)
	}
	entries := make([]iofs.DirEntry, 0, len(names))
	for _, name := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue // removed while listing
			}
			return nil, mapErr("list", rel+"/"+name, err)
		}
		entries = append(entries, iofs.FileInfoToDirEntry(&statInfo{name: name, st: st}))
	}
	slices.SortFunc(entries, func(a, b iofs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

// RenameNoReplace moves from to to (files or directories) and fails with an error wrapping
// fs.ErrExist if to exists. Both parents are resolved without following symlinks; the
// rename itself happens relative to those parent fds, so a symlink swapped in afterwards
// can't redirect it. On filesystems without RENAME_NOREPLACE it returns
// ErrNoReplaceUnsupported and changes nothing.
func (r *Root) RenameNoReplace(from, to string) error {
	if err := checkRel(from); err != nil {
		return err
	}
	if err := checkRel(to); err != nil {
		return err
	}
	fromDir, fromBase := splitParent(from)
	toDir, toBase := splitParent(to)
	ffd, err := r.openat(fromDir, unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(ffd) }()
	tfd, err := r.openat(toDir, unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(tfd) }()
	err = step(r.hook, Op{Kind: OpRenameNoReplace, Path: from, To: to}, func() error {
		return unix.Renameat2(ffd, fromBase, tfd, toBase, unix.RENAME_NOREPLACE)
	})
	if errors.Is(err, unix.EINVAL) {
		return fmt.Errorf("%w: rename %s -> %s", ErrNoReplaceUnsupported, from, to)
	}
	if err != nil {
		return mapErr("rename", from+" -> "+to, err)
	}
	return nil
}

func mapErr(op, rel string, err error) error {
	switch {
	case errors.Is(err, unix.ELOOP):
		return fmt.Errorf("%w: %s %s", ErrSymlink, op, rel)
	case errors.Is(err, unix.EXDEV):
		return fmt.Errorf("%w: %s %s", ErrEscape, op, rel)
	case errors.Is(err, unix.EEXIST):
		return &iofs.PathError{Op: op, Path: rel, Err: iofs.ErrExist}
	case errors.Is(err, unix.ENOENT):
		return &iofs.PathError{Op: op, Path: rel, Err: iofs.ErrNotExist}
	case errors.Is(err, unix.ENOTDIR):
		return fmt.Errorf("%w: %s %s", ErrNotDir, op, rel)
	default:
		return &iofs.PathError{Op: op, Path: rel, Err: err}
	}
}

// statInfo is an fs.FileInfo from an fstatat result.
type statInfo struct {
	name string
	st   unix.Stat_t
}

func (i *statInfo) Name() string       { return i.name }
func (i *statInfo) Size() int64        { return i.st.Size }
func (i *statInfo) ModTime() time.Time { return time.Unix(i.st.Mtim.Unix()) }
func (i *statInfo) IsDir() bool        { return i.Mode().IsDir() }
func (i *statInfo) Sys() any           { return &i.st }

func (i *statInfo) Mode() iofs.FileMode {
	m := iofs.FileMode(i.st.Mode & 0o777)
	switch i.st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		m |= iofs.ModeDir
	case unix.S_IFLNK:
		m |= iofs.ModeSymlink
	case unix.S_IFREG:
	case unix.S_IFIFO:
		m |= iofs.ModeNamedPipe
	case unix.S_IFSOCK:
		m |= iofs.ModeSocket
	case unix.S_IFCHR:
		m |= iofs.ModeDevice | iofs.ModeCharDevice
	case unix.S_IFBLK:
		m |= iofs.ModeDevice
	default:
		m |= iofs.ModeIrregular
	}
	return m
}
