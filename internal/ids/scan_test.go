//go:build linux

package ids

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAnUnchangedVaultChangesNothing(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A", "notes/b.md": "B"})
	before := e.row("a.md")
	planned := 0
	setKillPoint(t, func(p string) {
		if p == "scan-planned" {
			planned++
		}
	})
	if got := e.pass(); got != (Totals{}) || planned != 0 {
		t.Fatalf("a pass over an unchanged vault: %+v, %d writes planned; want nothing", got, planned)
	}
	if after := e.row("a.md"); after.ID != before.ID || after.Generation != 1 || after.Suspect {
		t.Fatalf("a.md: %+v, was %+v", after, before)
	}
}

// An outside edit bumps the generation and records the new hash; new stats with the same
// bytes (a touch) don't.
func TestAnOutsideEditBumpsTheGeneration(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A", "b.md": "B"})
	id := e.row("a.md").ID
	e.write("a.md", "edited outside")
	later := time.Now().Add(time.Hour)
	must(t, os.Chtimes(filepath.Join(e.dir, "b.md"), later, later))
	if got := e.pass(); got != (Totals{Bumps: 1}) {
		t.Fatalf("totals %+v, want one bump", got)
	}
	a := e.row("a.md")
	if sum := sha256.Sum256([]byte("edited outside")); a.ID != id || a.Generation != 2 || !bytes.Equal(a.SHA256, sum[:]) {
		t.Fatalf("a.md after an outside edit: %+v", a)
	}
	b := e.row("b.md")
	if b.Generation != 1 || b.Stat.MtimeNS != later.UnixNano() {
		t.Fatalf("b.md after a touch: %+v; want generation 1 and the new mtime", b)
	}
}

func TestANewFileGetsAnID(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	e.write("notes/new.md", "new")
	if got := e.pass(); got != (Totals{NewIDs: 1}) {
		t.Fatalf("totals %+v", got)
	}
	if r := e.row("notes/new.md"); r.Generation != 1 || r.ID == e.row("a.md").ID {
		t.Fatalf("notes/new.md: %+v", r)
	}
}

// Rule 1: an outside mv keeps the ID at once, even among identical files.
func TestAnOutsideMvKeepsTheIDAtOnce(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "same", "b.md": "same", "c.md": "same"})
	ida, idb := e.row("a.md").ID, e.row("b.md").ID
	e.mv("a.md", "moved/a.md")
	if got := e.pass(); got != (Totals{Moves: 1}) {
		t.Fatalf("totals %+v, want one move", got)
	}
	if r := e.row("moved/a.md"); r.ID != ida || r.Generation != 2 || r.Suspect {
		t.Fatalf("moved/a.md: %+v, want a.md's ID at generation 2", r)
	}
	e.noRow("a.md")
	if e.row("b.md").ID != idb {
		t.Fatal("b.md lost its ID")
	}
}

// Rule 2: a unique copy-and-delete holds the new path until the second pass, then the ID
// moves to it.
func TestACopyAndDeleteKeepsTheIDAfterTheSecondPass(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "unique", "b.md": "other"})
	id := e.row("a.md").ID
	e.cp("a.md", "copy.md")
	e.rm("a.md")
	if got := e.pass(); got != (Totals{Held: 1}) {
		t.Fatalf("first pass %+v, want one held path", got)
	}
	e.noRow("copy.md")
	if r := e.row("a.md"); !r.Suspect || !r.SuspectSince.Equal(e.clock) {
		t.Fatalf("a.md after the first pass: %+v, want suspect since now", r)
	}
	if h := e.holds(); len(h) != 1 || h[0].Path != "copy.md" || h[0].FileID != id {
		t.Fatalf("holds %+v", h)
	}
	e.after(SecondPass - time.Millisecond)
	if got := e.pass(); got != (Totals{}) {
		t.Fatalf("a pass before the 10 s: %+v, want nothing", got)
	}
	e.after(time.Millisecond)
	if got := e.pass(); got != (Totals{Moves: 1}) {
		t.Fatalf("the second pass %+v, want the move", got)
	}
	if r := e.row("copy.md"); r.ID != id || r.Generation != 2 || r.Suspect {
		t.Fatalf("copy.md: %+v, want a.md's ID at generation 2", r)
	}
	if h := e.holds(); len(h) != 0 {
		t.Fatalf("holds left: %+v", h)
	}
}

// A held path gets a new ID when the old path is back.
func TestAHeldPathGetsANewIDWhenTheOldPathIsBack(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "unique"})
	id := e.row("a.md").ID
	e.cp("a.md", "copy.md")
	e.aside("a.md")
	e.pass()
	e.back("a.md")
	e.after(time.Second)
	if got := e.pass(); got != (Totals{NewIDs: 1}) {
		t.Fatalf("totals %+v, want one new ID", got)
	}
	if r := e.row("a.md"); r.ID != id || r.Suspect || r.Generation != 1 {
		t.Fatalf("a.md: %+v, want its ID, live, generation 1", r)
	}
	if r := e.row("copy.md"); r.ID == id {
		t.Fatal("copy.md took a.md's ID")
	}
}

// A path missing for one pass only keeps its ID and generation.
func TestAPathMissingForOnePassKeepsItsID(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	id := e.row("a.md").ID
	e.aside("a.md")
	if got := e.pass(); got != (Totals{Suspects: 1}) {
		t.Fatalf("totals %+v, want one suspect", got)
	}
	e.back("a.md")
	e.after(SecondPass)
	e.pass()
	if r := e.row("a.md"); r.ID != id || r.Suspect || r.Generation != 1 {
		t.Fatalf("a.md: %+v", r)
	}
}

// A file really deleted is tombstoned by a pass at least 10 s after the first miss.
func TestADeletedFileIsTombstonedOnTheSecondPass(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	id := e.row("a.md").ID
	e.rm("a.md")
	e.pass()
	e.after(SecondPass / 2)
	if got := e.pass(); got != (Totals{}) {
		t.Fatalf("a pass 5 s later: %+v, want nothing", got)
	}
	e.after(SecondPass / 2)
	if got := e.pass(); got != (Totals{Tombstones: 1}) {
		t.Fatalf("totals %+v, want one tombstone", got)
	}
	e.noRow("a.md")
	if ts := e.tombstone(id); ts.LastPath != "a.md" || ts.LastGeneration != 1 || ts.Reason != "missing" || !ts.At.Equal(e.clock) {
		t.Fatalf("tombstone %+v", ts)
	}
}

// Two new copies of a deleted file: no rule matches, both get new IDs, and the old ID is
// tombstoned as an ambiguous rename.
func TestTwoCopiesOfADeletedFileGetNewIDs(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "unique"})
	id := e.row("a.md").ID
	e.cp("a.md", "x.md")
	e.cp("a.md", "y.md")
	e.rm("a.md")
	if got := e.pass(); got != (Totals{NewIDs: 2, Suspects: 1}) {
		t.Fatalf("first pass %+v", got)
	}
	e.after(SecondPass)
	e.pass()
	if ts := e.tombstone(id); ts.Reason != "ambiguous-rename" {
		t.Fatalf("tombstone %+v, want ambiguous-rename", ts)
	}
}

// A rename plus an edit gives a new ID; the old one is tombstoned as missing.
func TestARenamePlusAnEditGetsANewID(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	id := e.row("a.md").ID
	e.mv("a.md", "b.md")
	e.write("b.md", "A, edited")
	e.pass()
	if r := e.row("b.md"); r.ID == id {
		t.Fatal("b.md kept a.md's ID after an edit")
	}
	e.after(SecondPass)
	e.pass()
	if ts := e.tombstone(id); ts.Reason != "missing" {
		t.Fatalf("tombstone %+v", ts)
	}
}

// A folder the walk can't read fails the pass, and nothing under it is taken for missing.
func TestAWalkThatFailsChangesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	e := newEnv(t, map[string]string{"locked/a.md": "A", "b.md": "B"})
	locked := filepath.Join(e.dir, "locked")
	// Not listable, but its files can still be stat'ed: only the walk fails.
	must(t, os.Chmod(locked, 0o300))                  //nolint:gosec // G302: a folder without its read bit, under test
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // G302: a folder, restored so TempDir can remove it
	e.rm("b.md")
	if _, err := e.s.Pass(ctx); err == nil {
		t.Fatal("a pass over an unreadable folder succeeded")
	}
	if e.row("locked/a.md").Suspect || e.row("b.md").Suspect {
		t.Fatal("a failed pass marked suspects")
	}
}
