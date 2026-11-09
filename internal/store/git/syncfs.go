package git

import (
	"os"
	"path"
	"strings"

	"github.com/go-git/go-billy/v5"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// syncFS is the billy filesystem go-git gets for .git (ADR-0016, "Durability"). go-git
// never calls fsync: it writes a loose object to a temp file and renames it into
// objects/xx/, creating that directory if needed. syncFS makes each step durable:
//
//   - a file opened for writing is fsynced when it is closed;
//   - after a rename, the destination directory and its parent (which may have gained
//     objects/xx/) are fsynced, then the source directory.
//
// Syncs go through the vault's fs.Root on the same .git, so they are confined like every
// other store access and reported to the same hook in tests. Refs never go through here:
// SetBranch writes them itself.
type syncFS struct {
	billy.Filesystem
	root *fs.Root
}

func (s syncFS) Create(name string) (billy.File, error) {
	f, err := s.Filesystem.Create(name)
	return s.wrap(f, err)
}

func (s syncFS) OpenFile(name string, flag int, perm os.FileMode) (billy.File, error) {
	f, err := s.Filesystem.OpenFile(name, flag, perm)
	if flag&(os.O_WRONLY|os.O_RDWR) == 0 {
		return f, err
	}
	return s.wrap(f, err)
}

func (s syncFS) TempFile(dir, prefix string) (billy.File, error) {
	f, err := s.Filesystem.TempFile(dir, prefix)
	return s.wrap(f, err)
}

func (s syncFS) wrap(f billy.File, err error) (billy.File, error) {
	if err != nil {
		return nil, err
	}
	return syncFile{File: f, root: s.root}, nil
}

func (s syncFS) Rename(from, to string) error {
	if err := s.Filesystem.Rename(from, to); err != nil {
		return err
	}
	toDir := dirOf(to)
	for _, d := range []string{toDir, dirOf(toDir), dirOf(from)} {
		if err := s.root.SyncDir(d); err != nil {
			return err
		}
	}
	return nil
}

// dirOf returns the parent of a slash-separated path relative to .git ("." for the top).
func dirOf(p string) string {
	d := path.Dir(strings.TrimPrefix(path.Clean("/"+p), "/"))
	if d == "" || d == "/" {
		return "."
	}
	return d
}

// syncFile fsyncs its file before closing it. billy's chroot wrapper hides the underlying
// *os.File, so the sync goes through the root by name.
type syncFile struct {
	billy.File
	root *fs.Root
}

func (f syncFile) Close() error {
	name := strings.TrimPrefix(path.Clean("/"+f.Name()), "/")
	syncErr := f.root.SyncFile(name)
	closeErr := f.File.Close()
	if syncErr != nil {
		return syncErr // never retried: the commit fails (ADR-0003 §5)
	}
	return closeErr
}
