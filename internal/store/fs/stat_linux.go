//go:build linux

package fs

import (
	"crypto/sha256"
	"fmt"
	"io"
	iofs "io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// Stat returns the stat of the regular file rel, without following a symlink anywhere in
// the path.
func (r *Root) Stat(rel string) (Stat, error) {
	if err := checkRel(rel); err != nil {
		return Stat{}, err
	}
	fd, err := r.openat(rel, unix.O_PATH)
	if err != nil {
		return Stat{}, err
	}
	defer func() { _ = unix.Close(fd) }()
	return fstatRegular(fd, rel)
}

// Hash returns the full SHA-256 of the regular file rel, read in a stream (no size limit:
// a file over MaxFileSize is hashed like any other, ADR-0031), and its stat taken through
// the same descriptor before reading. The caller compares that stat with one taken after,
// to know whether a writer changed the file meanwhile.
func (r *Root) Hash(rel string) (Stat, [sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	if err := checkRel(rel); err != nil {
		return Stat{}, sum, err
	}
	// O_NONBLOCK: a FIFO swapped in after the caller listed a regular file must not block
	// the open; fstatRegular then refuses it.
	fd, err := r.openat(rel, unix.O_RDONLY|unix.O_NONBLOCK)
	if err != nil {
		return Stat{}, sum, err
	}
	f := os.NewFile(uintptr(fd), rel)
	defer func() { _ = f.Close() }()
	st, err := fstatRegular(fd, rel)
	if err != nil {
		return Stat{}, sum, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Stat{}, sum, mapErr("read", rel, err)
	}
	copy(sum[:], h.Sum(nil))
	return st, sum, nil
}

func fstatRegular(fd int, rel string) (Stat, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Stat{}, mapErr("stat", rel, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return Stat{}, fmt.Errorf("%w: %s", ErrNotRegular, rel)
	}
	return statFrom(&st), nil
}

func statFrom(st *unix.Stat_t) Stat {
	return Stat{Size: st.Size, Device: st.Dev, Inode: st.Ino, MtimeNS: st.Mtim.Nano(), CtimeNS: st.Ctim.Nano()}
}

// StatOf is the Stat in an Info that ReadDir listed (read with fstatat, no follow); ok is
// false for an Info from anywhere else.
func StatOf(info iofs.FileInfo) (Stat, bool) {
	st, ok := info.Sys().(*unix.Stat_t)
	if !ok {
		return Stat{}, false
	}
	return statFrom(st), true
}
