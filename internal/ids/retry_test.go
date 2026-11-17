//go:build linux

package ids

import (
	"os"
	"path/filepath"
	"testing"
)

// A read of a due hold runs a full pass; while full passes keep failing (a folder the walk
// can't read), reads don't walk the vault again until FullPassRetry has passed since the
// last failure, and are served meanwhile. Once the walk works, a read resolves the hold
// (#184, ADR-0037).
func TestAFailedFullPassIsNotRetriedByEveryRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	e := newEnv(t, map[string]string{"a.md": "unique", "locked/b.md": "B"})
	id := e.row("a.md").ID
	e.aside("a.md")
	e.pass() // a.md suspect
	e.cp2("unique", "copy.md")
	e.pass() // copy.md held for a.md
	if h := e.holds(); len(h) != 1 {
		t.Fatalf("holds %+v, want copy.md held", h)
	}
	walks := 0
	setKillPoint(t, func(p string) {
		if p == "scan-walk" {
			walks++
		}
	})
	locked := filepath.Join(e.dir, "locked")
	must(t, os.Chmod(locked, 0o300))                  //nolint:gosec // G302: a folder without its read bit, under test
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // G302: a folder, restored so TempDir can remove it
	e.after(SecondPass)

	if _, err := e.s.Seen(ctx, "copy.md"); err == nil || walks != 1 {
		t.Fatalf("the first read of the due hold: %v after %d walks; want the full pass's error after one", err, walks)
	}
	for range 3 {
		if wait, err := e.s.Seen(ctx, "copy.md"); err != nil || wait != 0 {
			t.Fatalf("a read inside the retry delay = %v, %v; want it served", wait, err)
		}
	}
	e.after(FullPassRetry - 1)
	if _, err := e.s.Seen(ctx, "copy.md"); err != nil || walks != 1 {
		t.Fatalf("reads inside the retry delay walked %d times (%v), want 1", walks, err)
	}
	e.after(1)
	if _, err := e.s.Seen(ctx, "copy.md"); err == nil || walks != 2 {
		t.Fatalf("after the retry delay: %v after %d walks; want a second failed walk", err, walks)
	}
	must(t, os.Chmod(locked, 0o700)) //nolint:gosec // G302: a folder, readable again
	e.after(FullPassRetry)
	if wait, err := e.s.Seen(ctx, "copy.md"); err != nil || wait != 0 || walks != 3 {
		t.Fatalf("once the walk works: %v, %v after %d walks", wait, err, walks)
	}
	if r := e.row("copy.md"); r.ID != id {
		t.Fatalf("copy.md: %+v, want a.md's ID", r)
	}
}

// A full pass that succeeds clears the failure: the next due hold's read runs one at once.
func TestASuccessfulPassClearsTheRetryDelay(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	e.s.fullFailed(e.clock)
	if !e.s.fullRetryWaits() {
		t.Fatal("a failure just now doesn't hold the reads' full passes back")
	}
	e.pass()
	if e.s.fullRetryWaits() {
		t.Fatal("a successful pass didn't clear the failure")
	}
}
