//go:build linux

package sql

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// syncModel records what a power loss would keep in the state directory: a folder's entries
// as of its last fsync, and a file's (an inode's) data as of its last fsync.
type syncModel struct {
	entries map[string]map[string]bool // folder -> its names at its last sync
	data    map[uint64]string          // inode -> its bytes at its last sync
}

func watchSyncs(t *testing.T) *syncModel {
	m := &syncModel{entries: map[string]map[string]bool{}, data: map[uint64]string{}}
	oldDir, oldFile := syncDir, syncFile
	t.Cleanup(func() { syncDir, syncFile = oldDir, oldFile })
	syncDir = func(path string) error {
		dir := filepath.Dir(path)
		des, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		m.entries[dir] = map[string]bool{}
		for _, d := range des {
			m.entries[dir][d.Name()] = true
		}
		return oldDir(path)
	}
	syncFile = func(f *os.File) error {
		var st syscall.Stat_t
		if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
			return err
		}
		b, err := os.ReadFile(f.Name())
		if err != nil {
			return err
		}
		m.data[st.Ino] = string(b)
		return oldFile(f)
	}
	return m
}

// kept reports what a power loss now would leave at path, under root (whose own entry
// is durable): its bytes, or ok false when an entry on the way or its data isn't durable.
func (m *syncModel) kept(t *testing.T, root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	must(t, err)
	at := root
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		if !m.entries[at][name] {
			return "", false
		}
		at = filepath.Join(at, name)
	}
	var st syscall.Stat_t
	must(t, syscall.Stat(path, &st))
	b, ok := m.data[st.Ino]
	return b, ok
}

// TestKeepIntentCopyIsDurable: once KeepIntentCopy returns, a power loss keeps the copy: its
// data, its name, and the two folders on its way.
func TestKeepIntentCopyIsDurable(t *testing.T) {
	state := t.TempDir()
	m := watchSyncs(t)
	must(t, KeepIntentCopy(state, opA, "temp", opener("draft")))
	if got, ok := m.kept(t, state, filepath.Join(state, "recovery", opA, "temp")); !ok || got != "draft" {
		t.Fatalf("a power loss right after the call keeps %q (%v); want the copy", got, ok)
	}
}

// TestKeepIntentCopyLeftovers: a .copying file a crash left is replaced, not kept or
// renamed; and when every name a side may take is used by other bytes, the call fails
// rather than overwrite one.
func TestKeepIntentCopyLeftovers(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "recovery", opA)
	must(t, os.MkdirAll(dir, 0o700))
	leftover := filepath.Join(state, "recovery", "."+opA+".temp.copying")
	must(t, os.WriteFile(leftover, []byte("half"), 0o600))
	must(t, KeepIntentCopy(state, opA, "temp", opener("draft")))
	if b, err := os.ReadFile(filepath.Join(dir, "temp")); err != nil || string(b) != "draft" { //nolint:gosec // G304: the test's own folder
		t.Fatalf("temp = %q, %v; want the new copy", b, err)
	}
	if _, err := os.Lstat(leftover); !os.IsNotExist(err) {
		t.Fatalf("the leftover copy is still there: %v", err)
	}
	for i := 0; i < maxCopies; i++ {
		must(t, KeepIntentCopy(state, opA, "target", opener(strings.Repeat("x", i+1))))
	}
	if err := KeepIntentCopy(state, opA, "target", opener("one more")); err == nil {
		t.Fatal("a copy past maxCopies names: nil, want an error")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "target")); err != nil || string(b) != "x" { //nolint:gosec // G304: the test's own folder
		t.Fatalf("target = %q, %v; want the first copy, untouched", b, err)
	}
}
