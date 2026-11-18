//go:build linux

package sql

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func opener(s string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(s)), nil }
}

// TestKeepIntentCopy: recovery's copies of an intent's sides go to recovery/<op-id>/, owner
// only. A copy already there with the same bytes is the one a crash left; with other bytes
// (the side changed between two recoveries), the new bytes get the next free name, so no
// copy is ever overwritten.
func TestKeepIntentCopy(t *testing.T) {
	state := t.TempDir()
	must(t, KeepIntentCopy(state, opA, "temp", opener("draft")))
	must(t, KeepIntentCopy(state, opA, "temp", opener("draft"))) // a repeat after a crash
	must(t, KeepIntentCopy(state, opA, "target", opener("theirs")))
	must(t, KeepIntentCopy(state, opA, "target", opener("theirs, changed")))
	dir := filepath.Join(state, "recovery", opA)
	got := map[string]string{}
	entries, err := os.ReadDir(dir)
	must(t, err)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // G304: the test's own state directory
		must(t, err)
		got[e.Name()] = string(b)
		fi, err := e.Info()
		must(t, err)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v, want 0600", e.Name(), fi.Mode().Perm())
		}
	}
	want := map[string]string{"temp": "draft", "target": "theirs", "target-1": "theirs, changed"}
	if len(got) != len(want) {
		t.Fatalf("recovery/%s holds %v, want %v", opA, got, want)
	}
	for n, c := range want {
		if got[n] != c {
			t.Errorf("%s = %q, want %q", n, got[n], c)
		}
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the copies' folder: %v, %v; want 0700", fi, err)
	}
	for _, bad := range []string{"", "..", "a/b", "rebuild-x"} {
		if err := KeepIntentCopy(state, bad, "temp", opener("x")); err == nil {
			t.Errorf("KeepIntentCopy with op ID %q: nil, want an error", bad)
		}
	}
}
