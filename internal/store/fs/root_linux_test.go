//go:build linux

package fs

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// newVault builds:
//
//	base/secret                  (outside the vault)
//	base/vault/dir/note.md
//	base/vault/link-inside  -> dir/note.md
//	base/vault/link-outside -> ../secret
//	base/vault/dirlink      -> dir
//	base/vault/escape-dir   -> ..
func newVault(t *testing.T) (*Root, string) {
	t.Helper()
	base := t.TempDir()
	vault := filepath.Join(base, "vault")
	mustDo(t, os.MkdirAll(filepath.Join(vault, "dir"), 0o700))
	mustDo(t, os.WriteFile(filepath.Join(vault, "dir", "note.md"), []byte("inside"), 0o600))
	mustDo(t, os.WriteFile(filepath.Join(base, "secret"), []byte("SECRET"), 0o600))
	mustDo(t, os.Symlink("dir/note.md", filepath.Join(vault, "link-inside")))
	mustDo(t, os.Symlink("../secret", filepath.Join(vault, "link-outside")))
	mustDo(t, os.Symlink("dir", filepath.Join(vault, "dirlink")))
	mustDo(t, os.Symlink("..", filepath.Join(vault, "escape-dir")))
	r, err := OpenRoot(vault)
	mustDo(t, err)
	t.Cleanup(func() { _ = r.Close() })
	return r, vault
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadFile(t *testing.T) {
	r, _ := newVault(t)
	tests := []struct {
		path    string
		want    string
		wantErr error
	}{
		{path: "dir/note.md", want: "inside"},
		{path: "link-inside", wantErr: ErrSymlink},
		{path: "link-outside", wantErr: ErrSymlink},
		{path: "dirlink/note.md", wantErr: ErrSymlink},
		{path: "escape-dir/secret", wantErr: ErrSymlink},
		{path: "dir", wantErr: ErrNotRegular},
		{path: "missing.md", wantErr: iofs.ErrNotExist},
		{path: "../secret", wantErr: ErrInvalidPath},
		{path: "/etc/passwd", wantErr: ErrInvalidPath},
		{path: "dir/../dir/note.md", wantErr: ErrInvalidPath},
		{path: "dir//note.md", wantErr: ErrInvalidPath},
		{path: "", wantErr: ErrInvalidPath},
		{path: ".", wantErr: ErrInvalidPath},
		{path: "dir\\note.md", wantErr: ErrInvalidPath},
		{path: "dir/note.md\x00", wantErr: ErrInvalidPath},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := r.ReadFile(tt.path)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if string(got) != tt.want {
				t.Fatalf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReadFileFIFO: a FIFO swapped in for a file (after the caller listed it as regular)
// is refused at once; opening it blocking would wait for a writer forever.
func TestReadFileFIFO(t *testing.T) {
	r, vault := newVault(t)
	mustDo(t, syscall.Mkfifo(filepath.Join(vault, "dir", "fifo"), 0o600))
	done := make(chan error, 1)
	go func() {
		_, err := r.ReadFile("dir/fifo")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("err = %v, want ErrNotRegular", err)
		}
	case <-time.After(5 * time.Second):
		// Unblock the reader so the test process can exit.
		if f, err := os.OpenFile(filepath.Join(vault, "dir", "fifo"), os.O_WRONLY, 0); err == nil {
			_ = f.Close()
		}
		t.Fatal("ReadFile blocked on a FIFO")
	}
}

func TestRenameNoReplace(t *testing.T) {
	tests := []struct {
		name     string
		from, to string
		wantErr  error
		check    func(t *testing.T, vault string)
	}{
		{name: "file to new name", from: "dir/note.md", to: "dir/renamed.md",
			check: wantContent("dir/renamed.md", "inside")},
		{name: "file into the root", from: "dir/note.md", to: "top.md",
			check: wantContent("top.md", "inside")},
		{name: "directory", from: "dir", to: "moved",
			check: wantContent("moved/note.md", "inside")},
		{name: "destination exists: nothing changes", from: "dir/note.md", to: "dir/other.md",
			wantErr: iofs.ErrExist, check: func(t *testing.T, vault string) {
				wantContent("dir/note.md", "inside")(t, vault)
				wantContent("dir/other.md", "other")(t, vault)
			}},
		{name: "source parent is a symlink", from: "dirlink/note.md", to: "x.md", wantErr: ErrSymlink},
		{name: "destination parent is a symlink", from: "dir/note.md", to: "escape-dir/stolen.md",
			wantErr: ErrSymlink, check: func(t *testing.T, vault string) {
				if _, err := os.Lstat(filepath.Join(vault, "..", "stolen.md")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("file escaped the vault: %v", err)
				}
			}},
		{name: "escape with ..", from: "dir/note.md", to: "../stolen.md", wantErr: ErrInvalidPath},
		{name: "missing source", from: "nope.md", to: "x.md", wantErr: iofs.ErrNotExist},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, vault := newVault(t)
			mustDo(t, os.WriteFile(filepath.Join(vault, "dir", "other.md"), []byte("other"), 0o600))
			err := r.RenameNoReplace(tt.from, tt.to)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.check != nil {
				tt.check(t, vault)
			}
		})
	}
}

func wantContent(rel, want string) func(t *testing.T, vault string) {
	return func(t *testing.T, vault string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(vault, rel))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", rel, got, err, want)
		}
	}
}
