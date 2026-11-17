//go:build linux

package fs

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCreateFile(t *testing.T) {
	tests := []struct {
		name    string
		rel     string
		wantErr error
	}{
		{"new file", "dir/new.md", nil},
		{"existing file", "dir/note.md", iofs.ErrExist},
		{"symlink as the name", "link-inside", iofs.ErrExist},
		{"parent through a symlink", "dirlink/new.md", ErrSymlink},
		{"parent escapes", "escape-dir/new.md", ErrSymlink},
		{"dot-dot", "../new.md", ErrInvalidPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, vault := newVault(t)
			err := r.CreateFile(tt.rel, []byte("hello"), 0o600)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("CreateFile(%q) = %v, want %v", tt.rel, err, tt.wantErr)
			}
			if tt.wantErr == nil {
				wantContent(tt.rel, "hello")(t, vault)
			}
		})
	}
	t.Run("never writes through a symlink", func(t *testing.T) {
		r, vault := newVault(t)
		if err := r.CreateFile("link-outside", []byte("pwned"), 0o600); !errors.Is(err, iofs.ErrExist) {
			t.Fatalf("CreateFile over a symlink = %v, want ErrExist", err)
		}
		wantContent("secret", "SECRET")(t, filepath.Dir(vault))
	})
}

// failFrom fails every step from the first one of kind from on with err, as a disk that
// started failing (err EIO) or a process that was killed there (every later step refused).
type failFrom struct {
	from    OpKind
	err     error
	failing bool
	only    bool // fail only the from step, so later steps (the cleanup) run
}

func (h *failFrom) Before(op Op) error {
	if op.Kind == h.from {
		h.failing = true
		return h.err
	}
	if h.failing && !h.only {
		return h.err
	}
	return nil
}

func (h *failFrom) After(Op, error) {}

// TestCreateFileRemovesAFileItCouldNotSync: a failed fsync removes the new file, as a failed
// write does, so a caller that retries doesn't leave one file per attempt (#189). A process
// killed at the fsync leaves it (the removal is a step the hook refuses), for the startup
// sweeps to find.
func TestCreateFileRemovesAFileItCouldNotSync(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook *failFrom
		left bool
	}{
		{"fsync fails", &failFrom{from: OpSyncFile, err: unix.EIO, only: true}, false},
		{"killed at the fsync", &failFrom{from: OpSyncFile, err: errors.New("killed")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r, err := OpenRoot(dir, WithHook(tc.hook))
			mustDo(t, err)
			t.Cleanup(func() { _ = r.Close() })
			if err := r.CreateFile("new.tmp", []byte("x"), 0o600); !errors.Is(err, tc.hook.err) {
				t.Fatalf("CreateFile with a failing fsync = %v, want %v", err, tc.hook.err)
			}
			_, err = os.Lstat(filepath.Join(dir, "new.tmp"))
			if left := err == nil; left != tc.left {
				t.Fatalf("after a failed fsync the file is left = %v, want %v (%v)", left, tc.left, err)
			}
		})
	}
}

// TestCreateFileModeAndPerm: CreateFileMode sets exactly the bits asked for, even ones the
// umask would clear, and Perm reads them back without following a symlink.
func TestCreateFileModeAndPerm(t *testing.T) {
	r, _ := newVault(t)
	for _, perm := range []uint32{0o600, 0o644, 0o664, 0o755} {
		rel := "dir/mode-" + strconv.FormatUint(uint64(perm), 8)
		mustDo(t, r.CreateFileMode(rel, []byte("x"), perm))
		if got, err := r.Perm(rel); err != nil || got != perm {
			t.Fatalf("Perm after CreateFileMode(%o) = %o, %v", perm, got, err)
		}
	}
	if _, err := r.Perm("link-outside"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Perm on a symlink = %v, want ErrSymlink", err)
	}
	if _, err := r.Perm("dir"); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Perm on a directory = %v, want ErrNotRegular", err)
	}
}

func TestRenameReplaces(t *testing.T) {
	r, vault := newVault(t)
	mustDo(t, r.CreateFile("dir/tmp", []byte("new"), 0o600))
	mustDo(t, r.Rename("dir/tmp", "dir/note.md"))
	wantContent("dir/note.md", "new")(t, vault)
	if _, err := os.Lstat(filepath.Join(vault, "dir", "tmp")); !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("temp file still present: %v", err)
	}
	if err := r.Rename("dir/note.md", "dirlink/x.md"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Rename into a symlinked parent = %v, want ErrSymlink", err)
	}
}

func TestMkdirAndSyncDir(t *testing.T) {
	r, vault := newVault(t)
	mustDo(t, r.Mkdir(".postelmd-tmp"))
	st, err := os.Stat(filepath.Join(vault, ".postelmd-tmp"))
	mustDo(t, err)
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v, want 0700", st.Mode().Perm())
	}
	if err := r.Mkdir(".postelmd-tmp"); !errors.Is(err, iofs.ErrExist) {
		t.Fatalf("second Mkdir = %v, want ErrExist", err)
	}
	mustDo(t, r.SyncDir("."))
	mustDo(t, r.SyncDir(".postelmd-tmp"))
	if err := r.SyncDir("dirlink"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("SyncDir on a symlink = %v, want ErrSymlink", err)
	}
}

func TestRemove(t *testing.T) {
	r, vault := newVault(t)
	mustDo(t, r.Remove("dir/note.md"))
	if _, err := os.Lstat(filepath.Join(vault, "dir", "note.md")); !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("file still present: %v", err)
	}
	if err := r.Remove("dir/note.md"); !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("second Remove = %v, want ErrNotExist", err)
	}
	if err := r.Remove("dirlink/note.md"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Remove through a symlinked parent = %v, want ErrSymlink", err)
	}
	if err := r.Remove("dir"); err == nil {
		t.Fatal("Remove removed a directory")
	}
	// Removing a symlink removes the link, never its target.
	mustDo(t, r.Remove("link-outside"))
	wantContent("secret", "SECRET")(t, filepath.Dir(vault))
}

func TestSyncFile(t *testing.T) {
	r, _ := newVault(t)
	mustDo(t, r.SyncFile("dir/note.md"))
	if err := r.SyncFile("dir"); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("SyncFile on a directory = %v, want ErrNotRegular", err)
	}
	if err := r.SyncFile("link-outside"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("SyncFile on a symlink = %v, want ErrSymlink", err)
	}
}

func TestReadDir(t *testing.T) {
	r, _ := newVault(t)
	entries, err := r.ReadDir(".")
	mustDo(t, err)
	var got []string
	for _, e := range entries {
		kind := "file"
		switch {
		case e.Type()&iofs.ModeSymlink != 0:
			kind = "symlink"
		case e.IsDir():
			kind = "dir"
		}
		got = append(got, e.Name()+":"+kind)
	}
	want := "dir:dir dirlink:symlink escape-dir:symlink link-inside:symlink link-outside:symlink"
	if strings.Join(got, " ") != want {
		t.Fatalf("ReadDir(.) = %v, want %s", got, want)
	}
	if _, err := r.ReadDir("dirlink"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("ReadDir through a symlink = %v, want ErrSymlink", err)
	}
	if _, err := r.ReadDir("dir/note.md"); err == nil {
		t.Fatal("ReadDir on a file succeeded")
	}
}

// Info must describe the entry in the root, not whatever the same name resolves to from
// the process's working directory.
func TestReadDirInfo(t *testing.T) {
	r, dir := newVault(t)
	t.Chdir(t.TempDir())
	mustDo(t, os.WriteFile("note.md", []byte("a decoy outside the root"), 0o600))
	entries, err := r.ReadDir("dir")
	mustDo(t, err)
	want, err := os.Lstat(filepath.Join(dir, "dir", "note.md"))
	mustDo(t, err)
	for _, e := range entries {
		if e.Name() != "note.md" {
			continue
		}
		info, err := e.Info()
		mustDo(t, err)
		if info.Size() != want.Size() || !info.ModTime().Equal(want.ModTime()) || !info.Mode().IsRegular() ||
			info.Mode().Perm() != want.Mode().Perm() || info.Name() != "note.md" {
			t.Fatalf("Info = %v %v %v, want %v %v %v", info.Size(), info.ModTime(), info.Mode(), want.Size(), want.ModTime(), want.Mode())
		}
		return
	}
	t.Fatal("note.md not listed")
}
