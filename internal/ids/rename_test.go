//go:build linux

package ids

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A hold is provisional: when the held row's own inode comes back at another path (rule 1),
// the ID goes there and the held copy gets a new ID, inside the 10 s or after them.
func TestAnInodeBackElsewhereBeatsAHold(t *testing.T) {
	for name, later := range map[string]time.Duration{"inside the 10 s": 3 * time.Second, "after": SecondPass + time.Second} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, map[string]string{"a.md": "unique"})
			id := e.row("a.md").ID
			e.aside("a.md")
			e.cp2("unique", "copy.md")
			e.pass()
			if h := e.holds(); len(h) != 1 || h[0].Path != "copy.md" {
				t.Fatalf("holds %+v, want copy.md held", h)
			}
			e.back("renamed.md")
			e.after(later)
			e.pass()
			if r := e.row("renamed.md"); r.ID != id || r.Suspect {
				t.Fatalf("renamed.md: %+v, want a.md's ID", r)
			}
			if c := e.row("copy.md"); c.ID == id {
				t.Fatal("the held copy got the ID its original came back with")
			}
			if h := e.holds(); len(h) != 0 {
				t.Fatalf("holds %+v", h)
			}
		})
	}
}

// The same through a read: the read of the original's new path takes the ID and lets the
// held copy go.
func TestSeenOfAnInodeBackElsewhereReleasesTheHold(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "unique"})
	id := e.row("a.md").ID
	e.aside("a.md")
	e.cp2("unique", "copy.md")
	e.pass()
	e.back("renamed.md")
	e.after(time.Second)
	if wait, err := e.s.Seen(ctx, "renamed.md"); err != nil || wait != 0 {
		t.Fatalf("Seen = %v, %v", wait, err)
	}
	if r := e.row("renamed.md"); r.ID != id {
		t.Fatalf("renamed.md: %+v, want a.md's ID", r)
	}
	if h := e.holds(); len(h) != 0 {
		t.Fatalf("holds %+v, want copy.md let go", h)
	}
}

// Rule 2's uniqueness is checked again before a hold becomes a move: a second copy of the
// bytes makes the rename ambiguous, so both copies get new IDs and the row is tombstoned as
// ambiguous-rename once it has been missing 10 s.
func TestASecondCopyMakesAHoldAmbiguous(t *testing.T) {
	for name, later := range map[string]time.Duration{"inside the 10 s": 3 * time.Second, "after": SecondPass} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, map[string]string{"a.md": "unique"})
			id := e.row("a.md").ID
			e.aside("a.md")
			e.cp2("unique", "copy.md")
			e.pass()
			e.cp2("unique", "copy2.md")
			e.after(later)
			e.pass()
			for _, p := range []string{"copy.md", "copy2.md"} {
				if r := e.row(p); r.ID == id {
					t.Fatalf("%s got a.md's ID though two paths have its bytes", p)
				}
			}
			if h := e.holds(); len(h) != 0 {
				t.Fatalf("holds %+v", h)
			}
			e.after(SecondPass)
			e.pass()
			if ts := e.tombstone(id); ts.Reason != "ambiguous-rename" {
				t.Fatalf("tombstone %+v, want ambiguous-rename", ts)
			}
		})
	}
}

// A read after a rename made outside PostelMD while it runs (no pass in between): the row is
// still live at the old path, which is gone, so rule 1 gives the new path its ID.
func TestSeenAfterARuntimeMove(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	id := e.row("a.md").ID
	e.mv("a.md", "b.md")
	if wait, err := e.s.Seen(ctx, "b.md"); err != nil || wait != 0 {
		t.Fatalf("Seen = %v, %v", wait, err)
	}
	if r := e.row("b.md"); r.ID != id {
		t.Fatalf("b.md: %+v, want a.md's ID", r)
	}
	e.noRow("a.md")
}

// Rule 2's "the only missing row" counts the live rows whose paths are gone too: x is
// suspect, y (the same bytes) was removed while PostelMD runs and is still live, and a new
// path with those bytes matches neither.
func TestSeenWithTwoMissingRowsOneLive(t *testing.T) {
	e := newEnv(t, map[string]string{"x.md": "dup", "y.md": "dup"})
	x, y := e.row("x.md").ID, e.row("y.md").ID
	e.aside("x.md")
	e.pass() // y.md is there: only x is suspect
	// y.md goes too, kept out of the vault so its inode isn't reused.
	must(t, os.Rename(filepath.Join(e.dir, "y.md"), filepath.Join(e.state, "y")))
	e.cp2("dup", "z.md")
	e.after(time.Second)
	if wait, err := e.s.Seen(ctx, "z.md"); err != nil || wait != 0 {
		t.Fatalf("Seen = %v, %v; want a new ID, not a hold", wait, err)
	}
	if r := e.row("z.md"); r.ID == x || r.ID == y {
		t.Fatalf("z.md took %s, one of two missing rows with its bytes", r.ID)
	}
}

// A clock stepped back leaves suspect_since in the future: the wait is capped at SecondPass
// and counts from now, not until the clock catches up.
func TestAClockSteppedBackDoesNotStretchTheWait(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "unique"})
	id := e.row("a.md").ID
	e.aside("a.md")
	e.pass() // suspect since the clock
	e.cp2("unique", "copy.md")
	e.after(-time.Hour)
	wait, err := e.s.Seen(ctx, "copy.md")
	if err != nil || wait != SecondPass {
		t.Fatalf("Seen = %v, %v; want %v", wait, err, SecondPass)
	}
	e.after(wait)
	if wait, err := e.s.Seen(ctx, "copy.md"); err != nil || wait != 0 {
		t.Fatalf("Seen after %v = %v, %v", SecondPass, wait, err)
	}
	if r := e.row("copy.md"); r.ID != id {
		t.Fatalf("copy.md: %+v, want a.md's ID", r)
	}
}

// A full pass after the clock stepped back tombstones a suspect 10 s after it first sees it
// in the future, not an hour later.
func TestAClockSteppedBackDoesNotDelayATombstone(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	id := e.row("a.md").ID
	e.rm("a.md")
	e.pass()
	e.after(-time.Hour)
	e.pass()
	e.after(SecondPass)
	e.pass()
	if ts := e.tombstone(id); ts.Reason != "missing" {
		t.Fatalf("tombstone %+v", ts)
	}
}
