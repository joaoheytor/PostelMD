//go:build linux

package faultfs_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// inode returns the inode number of path.
func inode(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Lstat(path)
	must(t, err)
	return fi.Sys().(*syscall.Stat_t).Ino
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: the test's own directory
	must(t, err)
	return string(b)
}

// TestMaterializeKeepsFileIdentity: a power loss that keeps a renamed file's new entry and
// its old one (the destination's folder synced, the source's not) leaves one file with two
// names. That is the state ADR-0033's same-inode rows recognize, so the model must write a
// hard link, not two copies.
func TestMaterializeKeepsFileIdentity(t *testing.T) {
	r, disk := open(t, -1)
	// A save's steps: the temp, its folder synced (so the temp's entry is durable), the
	// rename over the target, then only the target's folder synced.
	must(t, r.CreateFileMode("sub/tmp", []byte("new"), 0o600))
	must(t, r.SyncDir("sub"))
	must(t, r.Rename("sub/tmp", "a.md"))
	must(t, r.SyncDir("."))

	out := filepath.Join(t.TempDir(), "after")
	must(t, disk.Materialize(out))
	target, temp := filepath.Join(out, "a.md"), filepath.Join(out, "sub", "tmp")
	if read(t, target) != "new" || read(t, temp) != "new" {
		t.Fatalf("a.md = %q, sub/tmp = %q; want both new", read(t, target), read(t, temp))
	}
	if inode(t, target) != inode(t, temp) {
		t.Fatal("a.md and sub/tmp are two files; a power loss leaves one file with two names")
	}
}

// TestMaterializeWritesTheSurvivingState: what Materialize writes is AfterPowerLoss's
// content, with distinct files kept distinct.
func TestMaterializeWritesTheSurvivingState(t *testing.T) {
	r, disk := open(t, -1)
	must(t, r.CreateFile("sub/b.md", []byte("b"), 0o600))
	must(t, r.CreateFile("c.md", []byte("unsynced entry"), 0o600))
	must(t, r.SyncDir("sub"))
	out := filepath.Join(t.TempDir(), "after")
	must(t, disk.Materialize(out))
	want := disk.AfterPowerLoss()
	got := map[string]string{}
	must(t, filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == out {
			return err
		}
		rel, _ := filepath.Rel(out, p)
		if d.IsDir() {
			got[rel+"/"] = ""
			return nil
		}
		got[rel] = read(t, p)
		return nil
	}))
	if len(got) != len(want) {
		t.Fatalf("materialized %v, want %v", got, keys(want))
	}
	for p, c := range want {
		if g, ok := got[p]; !ok || g != string(c) {
			t.Errorf("%s = %q (present %v), want %q", p, g, ok, c)
		}
	}
	if inode(t, filepath.Join(out, "a.md")) == inode(t, filepath.Join(out, "sub", "b.md")) {
		t.Fatal("two distinct files share an inode")
	}
}

// TestMaterializeWritesSyncedDataOnly: an entry whose folder was synced but whose data never
// was survives a power loss empty, and one whose data changed after its last sync has the
// synced bytes. The steps go to the model directly: the store syncs every file it creates.
// Files are written 0600 and folders 0700, as the vault keeps them.
func TestMaterializeWritesSyncedDataOnly(t *testing.T) {
	_, disk := open(t, -1)
	disk.After(fs.Op{Kind: fs.OpCreate, Path: "sub/never-synced", Data: []byte("lost")}, nil)
	disk.After(fs.Op{Kind: fs.OpSyncDir, Path: "sub"}, nil)
	out := filepath.Join(t.TempDir(), "after")
	must(t, disk.Materialize(out))
	p := filepath.Join(out, "sub", "never-synced")
	if got := read(t, p); got != "" {
		t.Fatalf("a file whose data was never synced = %q; want it empty", got)
	}
	if got := read(t, filepath.Join(out, "a.md")); got != "old" {
		t.Fatalf("a.md = %q; want its synced bytes", got)
	}
	for path, want := range map[string]os.FileMode{p: 0o600, filepath.Join(out, "a.md"): 0o600, filepath.Join(out, "sub"): 0o700 | os.ModeDir} {
		fi, err := os.Lstat(path)
		must(t, err)
		if fi.Mode() != want {
			t.Errorf("%s: mode %v, want %v", filepath.Base(path), fi.Mode(), want)
		}
	}
}

// TestMaterializeAFolderInTwoPlaces: a folder can't be hard-linked, so a power loss that
// keeps a moved folder in both places writes it twice; its files stay one file each.
func TestMaterializeAFolderInTwoPlaces(t *testing.T) {
	r, disk := open(t, -1)
	must(t, r.Mkdir("d"))
	must(t, r.CreateFile("d/f.md", []byte("f"), 0o600))
	must(t, r.SyncDir("d"))
	must(t, r.SyncDir("."))
	must(t, r.RenameNoReplace("d", "sub/d"))
	must(t, r.SyncDir("sub"))
	out := filepath.Join(t.TempDir(), "after")
	must(t, disk.Materialize(out))
	a, b := filepath.Join(out, "d", "f.md"), filepath.Join(out, "sub", "d", "f.md")
	if read(t, a) != "f" || read(t, b) != "f" || inode(t, a) != inode(t, b) {
		t.Fatalf("the folder's file in both places: %q, %q; want one file", read(t, a), read(t, b))
	}
}
