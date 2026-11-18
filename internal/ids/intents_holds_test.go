//go:build linux

package ids

import (
	"testing"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// moveOnto begins an intent whose root is to (a folder move onto it), so it names to.
func (e *env) moveOnto(to string) {
	e.t.Helper()
	must(e.t, e.db.Begin(ctx, opSave, sqlstore.MoveIntent{From: "elsewhere", To: sqlstore.BytePath(to), Folder: true,
		Device: 1, Inode: 1, Mechanism: sqlstore.MechanismNoReplace}, e.clock))
}

// TestAHoldUnderAnIntentsPathWaits: a hold under an intent's root is neither used nor
// released by a pass, even its second; and Seen on a path outside every root leaves it as
// it is.
func TestAHoldUnderAnIntentsPathWaits(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "unique", "b.md": "other"})
	id := e.row("a.md").ID
	e.cp("a.md", "copy.md")
	e.rm("a.md")
	e.pass() // copy.md held for a.md's ID
	e.moveOnto("copy.md")
	e.after(SecondPass)
	e.pass()
	if h := e.holds(); len(h) != 1 || h[0].Path != "copy.md" || h[0].FileID != id {
		t.Fatalf("holds %+v; want copy.md's, untouched", h)
	}
	e.noRow("copy.md")
	e.cp("copy.md", "copy2.md")
	if _, err := e.s.Seen(ctx, "copy2.md"); err != nil {
		t.Fatal(err)
	}
	if h := e.holds(); len(h) != 1 || h[0].Path != "copy.md" || h[0].FileID != id {
		t.Fatalf("after Seen elsewhere, holds %+v; want copy.md's, untouched", h)
	}
	if r, err := e.db.FileByPath(ctx, "copy2.md"); err == nil && r.ID == id {
		t.Fatal("Seen gave a.md's ID, held under an intent, to copy2.md")
	}
}

// TestSeenElsewhereLeavesARowUnderAnIntentAlone: a.md's row is under an intent's root and its
// file moved out of it; Seen at the new path doesn't take the row there.
func TestSeenElsewhereLeavesARowUnderAnIntentAlone(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "unique"})
	a := e.row("a.md")
	e.saveIntent("a.md")
	e.mv("a.md", "b.md")
	if _, err := e.s.Seen(ctx, "b.md"); err != nil {
		t.Fatal(err)
	}
	if got := e.row("a.md"); got.ID != a.ID || got.Suspect || got.Generation != a.Generation {
		t.Fatalf("a.md = %+v; want its row untouched", got)
	}
	if got, err := e.db.FileByPath(ctx, "b.md"); err == nil && got.ID == a.ID {
		t.Fatal("Seen moved a.md's row, under an intent, to b.md")
	}
}
