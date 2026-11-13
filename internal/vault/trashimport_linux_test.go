//go:build linux

package vault

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TrashForImport reads the trash as ADR-0024's listing does, for ADR-0031's trash import:
// every layout an item can be in, and what is skipped.
func TestTrashForImport(t *testing.T) {
	const (
		s1 = "20240101T000000.000000000Z"
		s2 = "20240102T000000.000000000Z"
		s3 = "20240103T000000.000000000Z"
		s4 = "20240104T000000.000000000Z"
		s5 = "20240105T000000.000000000Z"
	)
	v, dir := newVault(t)
	trash := filepath.Join(dir, TrashDir)
	writeFiles(t, dir, map[string]string{
		TrashDir + "/" + s1 + "/a.md":         "a stamp folder holding a file (ADR-0022)",
		TrashDir + "/" + s1 + "-1/docs/x.md":  "a second delete in the same instant, a folder",
		TrashDir + "/" + s2 + "/one.md":       "a folder named like a stamp, several entries:",
		TrashDir + "/" + s2 + "/two.md":       "no layout reads it, so it isn't imported",
		TrashDir + "/" + s3 + "-notes/n.md":   "the old layout, a folder named notes",
		TrashDir + "/" + s3 + "-notes/m.md":   "with two entries",
		TrashDir + "/" + s4 + "-b.md":         "the old layout, a file",
		TrashDir + "/not-a-stamp.md":          "no delete makes this name",
		TrashDir + "/" + s5 + "-2/inside.txt": "",
	})
	must(t, os.Mkdir(filepath.Join(trash, s5), 0o700)) // empty: a crash's leftover
	must(t, os.Symlink("../a.md", filepath.Join(trash, s5+"-link.md")))
	must(t, syscall.Mkfifo(filepath.Join(trash, s5+"-fifo"), 0o600))
	must(t, os.Remove(filepath.Join(trash, s5+"-2", "inside.txt")))
	must(t, os.Symlink("/etc/hostname", filepath.Join(trash, s5+"-2", "inside.txt"))) // a stamp folder whose one entry is a symlink

	got, err := v.TrashForImport()
	must(t, err)
	at := func(s string) time.Time { tm, _ := time.Parse(stampLayout, s); return tm }
	want := []TrashItem{ // oldest first
		{ID: s1, Name: "a.md", Kind: KindFile, DeletedAt: at(s1)},
		{ID: s1 + "-1", Name: "docs", Kind: KindFolder, DeletedAt: at(s1)},
		{ID: s3 + "-notes", Name: "notes", Kind: KindFolder, DeletedAt: at(s3)},
		{ID: s4 + "-b.md", Name: "b.md", Kind: KindFile, DeletedAt: at(s4)},
	}
	if len(got) != len(want) {
		t.Fatalf("TrashForImport = %+v\nwant %+v", got, want)
	}
	for i := range want {
		g := got[i]
		g.Size = nil
		if g != want[i] {
			t.Errorf("item %d = %+v, want %+v", i, g, want[i])
		}
	}
}

func TestTrashForImportOfNoTrash(t *testing.T) {
	v, _ := newVault(t)
	if got, err := v.TrashForImport(); err != nil || len(got) != 0 {
		t.Fatalf("TrashForImport = %+v, %v; want nothing", got, err)
	}
}
