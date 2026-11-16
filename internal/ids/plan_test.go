//go:build linux

package ids

import (
	"bytes"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// The rename rules on cases a test can't make on disk at will (an inode reused by cp), with
// the planner's inputs written out: a row a.md that is missing, and one new path.
func TestRenameRulesOnTheSignals(t *testing.T) {
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	h := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	old := sqlstore.File{ID: sqlstore.NewID(now.Add(-time.Hour)), Path: "a.md", Generation: 3, SHA256: h(1),
		Stat: sqlstore.FileStat{Size: 5, Device: 1, Inode: 10, MtimeNS: 100, CtimeNS: 100}}
	for _, c := range []struct {
		name string
		sum  []byte
		stat fs.Stat
		want string // "move", "hold" or "new"
	}{
		{"mv: same hash, inode and mtime", h(1), fs.Stat{Size: 5, Device: 1, Inode: 10, MtimeNS: 100, CtimeNS: 200}, "move"},
		{"cp over the freed inode: another mtime", h(1), fs.Stat{Size: 5, Device: 1, Inode: 10, MtimeNS: 300, CtimeNS: 300}, "new"},
		{"the inode with other bytes", h(2), fs.Stat{Size: 5, Device: 1, Inode: 10, MtimeNS: 100, CtimeNS: 200}, "new"},
		{"another device", h(1), fs.Stat{Size: 5, Device: 2, Inode: 10, MtimeNS: 100, CtimeNS: 200}, "hold"},
		{"copy: same hash, another inode", h(1), fs.Stat{Size: 5, Device: 1, Inode: 11, MtimeNS: 300, CtimeNS: 300}, "hold"},
		{"another file", h(3), fs.Stat{Size: 9, Device: 1, Inode: 11, MtimeNS: 300, CtimeNS: 300}, "new"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &planner{now: now, rows: []sqlstore.File{old}, paths: []string{"b.md"}, totals: &Totals{},
				stat: func(path string) (fs.Stat, bool) { return c.stat, path == "b.md" },
				hash: func(string) (observed, bool) { return observed{c.stat, c.sum}, true }}
			units := p.plan()
			got := map[string]bool{}
			for _, u := range units {
				for _, ch := range u.changes {
					switch ch := ch.(type) {
					case sqlstore.UpdateFile:
						if ch.ID == old.ID && ch.NewPath == "b.md" && ch.Bump {
							got["move"] = true
						}
					case sqlstore.Hold:
						got["hold"] = ch.FileID == old.ID
					case sqlstore.InsertFile:
						got["new"] = ch.Path == "b.md"
					}
				}
			}
			if len(got) != 1 || !got[c.want] {
				t.Fatalf("decisions %v, want only %s", got, c.want)
			}
		})
	}
}

// Every decision rests on the paths it read: a move checks that the old path is still
// absent and the new one unchanged, so a file that comes back before the mutex is taken
// keeps its ID.
func TestAMoveChecksBothPaths(t *testing.T) {
	now := time.Now()
	st := fs.Stat{Size: 1, Device: 1, Inode: 10, MtimeNS: 1}
	old := sqlstore.File{ID: sqlstore.NewID(now), Path: "a.md", Generation: 1, SHA256: bytes.Repeat([]byte{1}, 32),
		Stat: fileStat(st)}
	p := &planner{now: now, rows: []sqlstore.File{old}, paths: []string{"b.md"}, totals: &Totals{},
		stat: func(path string) (fs.Stat, bool) { return st, path == "b.md" },
		hash: func(string) (observed, bool) { return observed{st, old.SHA256}, true }}
	units := p.plan()
	if len(units) != 1 {
		t.Fatalf("units %+v", units)
	}
	want := []check{{"a.md", false, fs.Stat{}}, {"b.md", true, st}}
	if c := units[0].checks; len(c) != 2 || c[0] != want[0] || c[1] != want[1] {
		t.Fatalf("checks %+v, want %+v", c, want)
	}
}
