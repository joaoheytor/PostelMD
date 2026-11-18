//go:build linux

package ids

import (
	"testing"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// The scan leaves alone every path an open or flagged intent names (ADR-0033, "The scan
// and intents"): those paths are the intent's until recovery or the operator ends it.

const opSave = "00112233445566778899aabbccddeeff"

func (e *env) saveIntent(p string) {
	e.t.Helper()
	r := e.row(p)
	must(e.t, e.db.Begin(ctx, opSave, sqlstore.SaveIntent{FileID: r.ID, Path: sqlstore.BytePath(p), BaseGeneration: r.Generation,
		BaseSHA256: r.SHA256, NewSHA256: make([]byte, 32), Stat: r.Stat}, e.clock))
}

func TestTheScanLeavesAnIntentsMissingPathAlone(t *testing.T) {
	for _, flagged := range []bool{false, true} {
		e := newEnv(t, map[string]string{"a.md": "alpha", "b.md": "beta"})
		a := e.row("a.md")
		e.saveIntent("a.md")
		if flagged {
			must(t, e.db.Flag(ctx, opSave))
		}
		e.rm("a.md")
		e.pass()
		e.after(SecondPass)
		e.pass()
		if got := e.row("a.md"); got.ID != a.ID || got.Suspect {
			t.Fatalf("flagged %v: a.md = %+v; want its row untouched, not suspect", flagged, got)
		}
	}
}

func TestTheScanGivesNoIDUnderAnIntentsPath(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "alpha"})
	a := e.row("a.md")
	must(t, e.db.Begin(ctx, opSave, sqlstore.MoveIntent{From: "a.md", To: "dest", Folder: true, Device: a.Stat.Device,
		Inode: a.Stat.Inode, Files: []sqlstore.Affected{{ID: a.ID, Generation: a.Generation, Path: "a.md"}},
		Mechanism: sqlstore.MechanismNoReplace}, e.clock))
	e.write("dest/new.md", "new")
	e.write("other.md", "other")
	e.pass()
	e.noRow("dest/new.md")
	e.row("other.md") // the rest of the vault is scanned as ever
	if wait, err := e.s.Seen(ctx, "dest/new.md"); err != nil || wait != 0 {
		t.Fatalf("Seen under an intent's path = %v, %v", wait, err)
	}
	e.noRow("dest/new.md")
}

// TestTheScanEndsAtTheIntentsEnd: once the intent is gone, the next pass decides as ever.
func TestTheScanDecidesOnceTheIntentEnds(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "alpha"})
	a := e.row("a.md")
	e.saveIntent("a.md")
	e.rm("a.md")
	e.pass()
	must(t, e.db.Drop(ctx, opSave))
	e.pass()
	if got, err := e.db.FileByID(ctx, a.ID); err != nil || !got.Suspect {
		t.Fatalf("after the intent ended: %+v, %v; want a.md's row suspect", got, err)
	}
}
