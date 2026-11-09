//go:build linux

package fs

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// CreateFile creates rel exclusively (it fails with an error wrapping fs.ErrExist if rel
// exists), writes data and fsyncs it. The new directory entry is not durable until the
// caller syncs the parent with SyncDir (ADR-0003 §5). On a write or sync error the partial
// file is removed. It is reported as two steps, OpCreate then OpSyncFile, so a crash between
// writing and syncing can be tested.
func (r *Root) CreateFile(rel string, data []byte, perm uint32) error {
	if err := checkRel(rel); err != nil {
		return err
	}
	dir, base := splitParent(rel)
	dfd, err := r.openat(dir, unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dfd) }()
	fd := -1
	err = step(r.hook, Op{Kind: OpCreate, Path: rel, Data: data}, func() error {
		fd, err = unix.Openat(dfd, base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, perm)
		if err != nil {
			return err
		}
		if werr := writeAll(fd, data); werr != nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(dfd, base, 0)
			return werr
		}
		return nil
	})
	if err != nil {
		return mapErr("create", rel, err)
	}
	defer func() { _ = unix.Close(fd) }()
	err = step(r.hook, Op{Kind: OpSyncFile, Path: rel}, func() error { return unix.Fsync(fd) })
	if err != nil {
		// Never retry a failed fsync (ADR-0003 §5): the caller fails the operation.
		return mapErr("sync", rel, err)
	}
	return nil
}

func writeAll(fd int, data []byte) error {
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// Rename moves from to to, atomically replacing to if it exists. It is for installing a
// synced temp file over a note (ADR-0003 §5); structural moves use RenameNoReplace.
func (r *Root) Rename(from, to string) error {
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
	err = step(r.hook, Op{Kind: OpRename, Path: from, To: to}, func() error {
		return unix.Renameat(ffd, fromBase, tfd, toBase)
	})
	if err != nil {
		return mapErr("rename", from+" -> "+to, err)
	}
	return nil
}

// Mkdir creates the directory rel with mode 0700 (ADR-0003 §5: `.postelmd-tmp/`). Like a
// new file, its entry is durable only after SyncDir on its parent.
func (r *Root) Mkdir(rel string) error {
	if err := checkRel(rel); err != nil {
		return err
	}
	dir, base := splitParent(rel)
	dfd, err := r.openat(dir, unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dfd) }()
	err = step(r.hook, Op{Kind: OpMkdir, Path: rel}, func() error { return unix.Mkdirat(dfd, base, 0o700) })
	if err != nil {
		return mapErr("mkdir", rel, err)
	}
	return nil
}

// SyncDir makes the entries of directory rel durable ("." is the root). Every change to a
// directory's entries (create, rename, mkdir) needs it before the change counts as done.
func (r *Root) SyncDir(rel string) error {
	if rel != "." {
		if err := checkRel(rel); err != nil {
			return err
		}
	}
	// fsync needs a real descriptor; O_PATH descriptors can't be synced.
	fd, err := r.openat(rel, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	err = step(r.hook, Op{Kind: OpSyncDir, Path: rel}, func() error { return unix.Fsync(fd) })
	if err != nil {
		return mapErr("sync", rel, err)
	}
	return nil
}

// Remove unlinks the file rel (never a directory). Vault deletes never use it: they move
// items to the trash (ADR-0003 §2). It is for app-owned files such as a git ref lock.
// Like any entry change, it is durable only after SyncDir on the parent.
func (r *Root) Remove(rel string) error {
	if err := checkRel(rel); err != nil {
		return err
	}
	dir, base := splitParent(rel)
	dfd, err := r.openat(dir, unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dfd) }()
	err = step(r.hook, Op{Kind: OpRemove, Path: rel}, func() error { return unix.Unlinkat(dfd, base, 0) })
	if err != nil {
		return mapErr("remove", rel, err)
	}
	return nil
}

// SyncFile fsyncs the regular file rel through a fresh descriptor (on Linux, fsync flushes
// the file whatever descriptor wrote it). It is for files written by code that can't sync
// them itself, such as go-git's object writer (ADR-0016, "Durability").
func (r *Root) SyncFile(rel string) error {
	if err := checkRel(rel); err != nil {
		return err
	}
	fd, err := r.openat(rel, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return mapErr("stat", rel, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%w: %s", ErrNotRegular, rel)
	}
	err = step(r.hook, Op{Kind: OpSyncFile, Path: rel}, func() error { return unix.Fsync(fd) })
	if err != nil {
		return mapErr("sync", rel, err)
	}
	return nil
}
