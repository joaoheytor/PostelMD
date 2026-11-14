//go:build linux

package fs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
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

// A canceled hash stops within one chunk, whatever the file's size: asserted by the bytes
// read, never by time (CLAUDE.md, Testing conventions).
func TestHashStopsWhenCanceled(t *testing.T) {
	r, vault := newVault(t)
	f, err := os.Create(filepath.Join(vault, "huge.bin"))
	mustDo(t, err)
	mustDo(t, errors.Join(f.Truncate(8<<30), f.Close())) // sparse: 8 GiB that cost nothing
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.Hash(ctx, "huge.bin"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Hash = %v, want context.Canceled", err)
	}
	// The copy itself: canceled during the first chunk, it reads at most one more.
	ctx, cancel = context.WithCancel(context.Background())
	src := &endless{cancelAt: 1, cancel: cancel}
	n, err := copyCtx(ctx, io.Discard, src)
	if !errors.Is(err, context.Canceled) || n > 2*hashChunk || src.read != n {
		t.Fatalf("copyCtx = %d bytes, %v; want context.Canceled within two chunks", n, err)
	}
	if n, err := copyCtx(context.Background(), io.Discard, bytes.NewReader(make([]byte, hashChunk+1))); err != nil || n != hashChunk+1 {
		t.Fatalf("copyCtx of a chunk and a byte = %d, %v", n, err)
	}
}

// endless is a reader that never ends; it calls cancel once it has served cancelAt bytes.
type endless struct {
	read, cancelAt int64
	cancel         func()
}

func (e *endless) Read(p []byte) (int, error) {
	e.read += int64(len(p))
	if e.read >= e.cancelAt {
		e.cancel()
	}
	return len(p), nil
}

func TestHash(t *testing.T) {
	r, vault := newVault(t)
	st, sum, err := r.Hash(context.Background(), "dir/note.md")
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
	_, sum, err = r.Hash(context.Background(), "big.bin")
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
		if _, _, err := r.Hash(context.Background(), p); !errors.Is(err, wantErr) {
			t.Errorf("Hash(%q) = %v, want %v", p, err, wantErr)
		}
	}
}
