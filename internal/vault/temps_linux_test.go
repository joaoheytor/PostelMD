package vault

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// TestTakeTemps: regular temps are kept, then removed; the marker stays; a folder, a symlink
// and a FIFO are left in place with their kind, never opened (the FIFO would block); a Keep
// that fails leaves its temp.
func TestTakeTemps(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, TmpDir)
	mustDo(t, os.Mkdir(tmp, 0o700))
	mustDo(t, os.WriteFile(filepath.Join(tmp, ownerMarker), nil, 0o600))
	mustDo(t, os.WriteFile(filepath.Join(tmp, "a"), []byte("one"), 0o600))
	mustDo(t, os.WriteFile(filepath.Join(tmp, "b"), []byte("two"), 0o600))
	mustDo(t, os.Mkdir(filepath.Join(tmp, "d"), 0o700))
	mustDo(t, os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(tmp, "l")))
	mustDo(t, syscall.Mkfifo(filepath.Join(tmp, "f"), 0o600))

	kept := map[string]string{}
	var removed, skipped []string
	h := TempHandler{
		Keep: func(name string, src io.Reader) error {
			if name == "b" && kept["b"] == "" {
				kept["b"] = "failed once"
				return errors.New("disk full")
			}
			data, err := io.ReadAll(src)
			kept[name] = string(data)
			return err
		},
		Removed: func(name string) { removed = append(removed, name) },
		Skipped: func(name, kind string) { skipped = append(skipped, name+":"+kind) },
	}
	if n, err := TakeTemps(dir, h); err == nil || n != 1 {
		t.Fatalf("TakeTemps with a failing Keep = %d, %v", n, err)
	}
	if _, err := os.Lstat(filepath.Join(tmp, "b")); err != nil {
		t.Fatalf("the temp whose Keep failed is gone: %v", err)
	}
	skipped = nil
	n, err := TakeTemps(dir, h)
	if err != nil || n != 1 {
		t.Fatalf("TakeTemps = %d, %v", n, err)
	}
	if kept["a"] != "one" || kept["b"] != "two" || !slices.Equal(removed, []string{"a", "b"}) {
		t.Fatalf("kept %v, removed %v", kept, removed)
	}
	slices.Sort(skipped)
	if want := []string{"d:folder", "f:special", "l:symlink"}; !slices.Equal(skipped, want) {
		t.Fatalf("skipped %v, want %v", skipped, want)
	}
	es, err := os.ReadDir(tmp)
	mustDo(t, err)
	var left []string
	for _, e := range es {
		left = append(left, e.Name())
	}
	if want := []string{ownerMarker, "d", "f", "l"}; !slices.Equal(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
}

// TestTakeTempsWithoutAFolder: no temp folder holds nothing; one that is a symlink is
// refused, with the target untouched.
func TestTakeTempsWithoutAFolder(t *testing.T) {
	h := TempHandler{Keep: func(string, io.Reader) error { t.Fatal("kept"); return nil }, Removed: func(string) {},
		Skipped: func(string, string) {}}
	dir := t.TempDir()
	if n, err := TakeTemps(dir, h); n != 0 || err != nil {
		t.Fatalf("no temp folder: %d, %v", n, err)
	}
	target := t.TempDir()
	mustDo(t, os.WriteFile(filepath.Join(target, "x"), []byte("x"), 0o600))
	mustDo(t, os.Symlink(target, filepath.Join(dir, TmpDir)))
	if _, err := TakeTemps(dir, h); !errors.Is(err, ErrBadMarker) {
		t.Fatalf("a symlinked temp folder: %v, want ErrBadMarker", err)
	}
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
