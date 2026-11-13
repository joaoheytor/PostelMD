//go:build linux

package fs

import (
	"bytes"
	"crypto/sha256"
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestStat(t *testing.T) {
	r, vault := newVault(t)
	path := filepath.Join(vault, "dir", "note.md")
	when := time.Date(2025, 1, 2, 3, 4, 5, 123456789, time.UTC)
	mustDo(t, os.Chtimes(path, when, when))
	st, err := r.Stat("dir/note.md")
	mustDo(t, err)
	var raw syscall.Stat_t
	mustDo(t, syscall.Lstat(path, &raw))
	want := Stat{Size: 6, Device: raw.Dev, Inode: raw.Ino, MtimeNS: when.UnixNano(), CtimeNS: raw.Ctim.Nano()}
	if st != want {
		t.Fatalf("Stat = %+v, want %+v", st, want)
	}
	for p, wantErr := range map[string]error{
		"link-inside":       ErrSymlink,
		"dirlink/note.md":   ErrSymlink,
		"escape-dir/secret": ErrSymlink,
		"dir":               ErrNotRegular,
		"missing.md":        iofs.ErrNotExist,
		"../secret":         ErrInvalidPath,
	} {
		if _, err := r.Stat(p); !errors.Is(err, wantErr) {
			t.Errorf("Stat(%q) = %v, want %v", p, err, wantErr)
		}
	}
}

// StatOf reads a listed entry's Info the same way Stat reads the file.
func TestStatOf(t *testing.T) {
	r, _ := newVault(t)
	es, err := r.ReadDir("dir")
	mustDo(t, err)
	info, err := es[0].Info()
	mustDo(t, err)
	got, ok := StatOf(info)
	want, err := r.Stat("dir/note.md")
	mustDo(t, err)
	if !ok || got != want {
		t.Fatalf("StatOf = %+v, %v; Stat = %+v", got, ok, want)
	}
	if _, ok := StatOf(fakeInfo{}); ok {
		t.Fatal("StatOf took an Info that isn't from ReadDir")
	}
}

type fakeInfo struct{ iofs.FileInfo }

func (fakeInfo) Sys() any { return nil }

func TestHash(t *testing.T) {
	r, vault := newVault(t)
	st, sum, err := r.Hash("dir/note.md")
	mustDo(t, err)
	if want := sha256.Sum256([]byte("inside")); sum != want {
		t.Fatalf("Hash = %x, want %x", sum, want)
	}
	if want, _ := r.Stat("dir/note.md"); st != want {
		t.Fatalf("Hash's stat = %+v, Stat = %+v", st, want)
	}
	// Past the read limit, streamed like any other (ADR-0031).
	big := bytes.Repeat([]byte("x"), MaxFileSize+1)
	mustDo(t, os.WriteFile(filepath.Join(vault, "big.bin"), big, 0o600))
	_, sum, err = r.Hash("big.bin")
	mustDo(t, err)
	if want := sha256.Sum256(big); sum != want {
		t.Fatal("Hash of a file over the read limit is wrong")
	}
	mustDo(t, syscall.Mkfifo(filepath.Join(vault, "fifo"), 0o600))
	for p, wantErr := range map[string]error{
		"link-inside": ErrSymlink,
		"dir":         ErrNotRegular,
		"fifo":        ErrNotRegular, // opened without blocking, then refused
		"missing.md":  iofs.ErrNotExist,
	} {
		if _, _, err := r.Hash(p); !errors.Is(err, wantErr) {
			t.Errorf("Hash(%q) = %v, want %v", p, err, wantErr)
		}
	}
}
