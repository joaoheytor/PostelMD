//go:build linux

package vault

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRetryOfALandedSave: the first save's rename landed but its response was lost, so the
// client retries with its old If-Match. The note already holds exactly those bytes, so the
// retry succeeds with the current revision instead of a conflict whose two sides are the same
// (ADR-0003 §3 and §5). Different content with the stale base is still a conflict.
func TestRetryOfALandedSave(t *testing.T) {
	v, dir := newVault(t)
	base := RevOf([]byte("A"))
	if _, err := v.Save(ctx, "notes/a.md", base, []byte("B")); err != nil {
		t.Fatal(err)
	}
	rev, err := v.Save(ctx, "notes/a.md", base, []byte("B"))
	if err != nil || rev != RevOf([]byte("B")) {
		t.Fatalf("retry with the old base = %s, %v; want %s", rev, err, RevOf([]byte("B")))
	}
	if _, err := v.Save(ctx, "notes/a.md", base, []byte("C")); !errors.Is(err, ErrConflict) {
		t.Fatalf("a different save with the old base = %v, want ErrConflict", err)
	}
	if got := content(t, dir, "notes/a.md"); got != "B" {
		t.Fatalf("note = %q, want B", got)
	}
}

// TestSaveKeepsTheNotesMode: a save replaces the note with a temp file that took the note's
// own permission bits first (ADR-0003 §5), so a 0644 note stays readable by the host's other
// tools and a 0755 script stays executable. Bits the umask would clear (0664) survive too.
func TestSaveKeepsTheNotesMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o755, 0o664, 0o600} {
		t.Run(mode.String(), func(t *testing.T) {
			v, dir := newVault(t)
			p := filepath.Join(dir, "notes", "a.md")
			must(t, os.Chmod(p, mode))
			if _, err := v.Save(ctx, "notes/a.md", RevOf([]byte("A")), []byte("B")); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(p)
			must(t, err)
			if fi.Mode().Perm() != mode || content(t, dir, "notes/a.md") != "B" {
				t.Fatalf("after a save: mode %v, content %q; want %v, B", fi.Mode().Perm(), content(t, dir, "notes/a.md"), mode)
			}
		})
	}
}

// TestCreateMakesPrivateNotes: a new note is 0600, like every file PostelMD creates in /data.
func TestCreateMakesPrivateNotes(t *testing.T) {
	v, dir := newVault(t)
	if _, err := v.Create(ctx, "notes/new.md", []byte("N")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "notes", "new.md")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("new note: %v, %v; want 0600", fi, err)
	}
}
