//go:build linux

package fs

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"testing"
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
