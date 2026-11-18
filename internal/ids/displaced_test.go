//go:build linux

package ids

import (
	"crypto/sha256"
	"testing"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// A PostelMD create onto a path whose row is stale (its file went outside since the last
// scan) vacates that row instead of tombstoning it (ADR-0033, "Giving a path a row"); the
// scan then decides for it by ADR-0031's rules, as for any missing row.

// createOnto records a create of p with content, as the create operation's completion does
// once its file is on disk: an intent, then CompleteCreate with the new file's stat.
func (e *env) createOnto(p, content string) string {
	e.t.Helper()
	e.write(p, content)
	st, ok, err := e.v.StatPath(p)
	if err != nil || !ok {
		e.t.Fatalf("StatPath(%q) = %v, %v", p, ok, err)
	}
	sum := sha256.Sum256([]byte(content))
	op := "0123456789abcdef0123456789abcdef"
	must(e.t, e.db.Begin(ctx, op, sqlstore.CreateIntent{Path: sqlstore.BytePath(p), NewSHA256: sum[:]}, e.clock))
	id, err := e.db.CompleteCreate(ctx, op, fileStat(st), e.clock)
	must(e.t, err)
	return id
}

func TestACreateOntoARenamedFilesPathKeepsItsID(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "alpha"})
	a := e.row("a.md")
	e.mv("a.md", "b.md") // outside PostelMD, before any scan
	created := e.createOnto("a.md", "new")
	e.pass()
	if got := e.row("b.md"); got.ID != a.ID {
		t.Fatalf("b.md has ID %s; want a.md's %s (rule 1)", got.ID, a.ID)
	}
	if got := e.row("a.md"); got.ID != created {
		t.Fatalf("a.md has ID %s; want the created %s", got.ID, created)
	}
	// Moved back onto a real path, it is an ordinary row: its tombstone names that path.
	e.rm("b.md")
	e.pass()
	e.after(SecondPass)
	e.pass()
	if ts := e.tombstone(a.ID); ts.LastPath != "b.md" {
		t.Fatalf("the tombstone: %+v; want b.md", ts)
	}
}

func TestACreateOntoACopiedFilesPathKeepsItsID(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "alpha"})
	a := e.row("a.md")
	e.cp("a.md", "c.md") // a new inode: rule 2, by hash
	e.rm("a.md")
	e.createOnto("a.md", "new")
	e.pass()
	e.after(SecondPass)
	e.pass()
	if got := e.row("c.md"); got.ID != a.ID {
		t.Fatalf("c.md has ID %s; want a.md's %s (rule 2)", got.ID, a.ID)
	}
}

func TestACreateOntoADeletedFilesPathTombstonesItInTwoPasses(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "alpha"})
	a := e.row("a.md")
	e.rm("a.md")
	e.createOnto("a.md", "new")
	e.pass()
	if got, err := e.db.FileByID(ctx, a.ID); err != nil || !got.Suspect {
		t.Fatalf("after one pass: %+v, %v; want the ID still suspect", got, err)
	}
	e.after(SecondPass)
	e.pass()
	if ts := e.tombstone(a.ID); ts.LastPath != "a.md" || ts.Reason != "missing" {
		t.Fatalf("the tombstone: %+v; want a.md, missing", ts)
	}
}
