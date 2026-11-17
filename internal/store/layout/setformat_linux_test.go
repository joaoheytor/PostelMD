//go:build linux

package layout

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSetFormat: it replaces the format file (and a leftover temp), and refuses a format this
// build doesn't write.
func TestSetFormat(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, formatFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, formatNew), []byte("left"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetFormat(state, FormatVersion); err != nil {
		t.Fatal(err)
	}
	if got := readFormat(t, state); got != "1\n" {
		t.Fatalf("format = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(state, formatNew)); !os.IsNotExist(err) {
		t.Fatalf("the temp is left: %v", err)
	}
	for _, n := range []int{0, MaxFormat + 1} {
		if err := SetFormat(state, n); err == nil {
			t.Fatalf("SetFormat(%d) wrote it", n)
		}
	}
	if got := readFormat(t, state); got != "1\n" {
		t.Fatalf("a refused SetFormat changed the format to %q", got)
	}
}
