//go:build linux

package intents

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

type crashRow struct {
	name          string
	target, temp  string // as in TestRecoverASave
	forward, kept bool   // the outcome: rolled forward; the temp copied
}

var crashRows = []crashRow{
	{"drop", "base", "ne", false, false},
	{"drop keeping the temp", "other", "new", false, true},
	{"roll forward", "new", "", true, false},
	{"roll forward over a second name", "new", "=", true, false},
}

// TestRecoveryCrashEverywhere kills recovery itself at each of its vault steps n, for a row
// of each action that writes, then kills the next start's recovery at each of its steps m
// too (repeated crashes), and lets a third start finish: it ends where an uninterrupted
// recovery does. Each action's steps are ordered so that every prefix of them is a state its
// table already handles, so recovery needs no intent of its own. A power loss at each step n
// is checked the same way, from what the disk would keep.
func TestRecoveryCrashEverywhere(t *testing.T) {
	for _, row := range crashRows {
		for n := 0; ; n++ {
			crashed := false
			for m := 0; ; m++ {
				e := setUpCrash(t, row)
				disk := e.kill(n)
				crashed = disk.Crashed()
				if !crashed {
					t.Run(row.name+"/uninterrupted", func(t *testing.T) { e.checkRecovered(t, row, e.dir) })
					break
				}
				again := e.kill(m)
				t.Run(row.name+"/process, twice", func(t *testing.T) { e.checkRecovered(t, row, e.dir) })
				if !again.Crashed() {
					break
				}
			}
			if !crashed {
				if n < 2 {
					t.Fatalf("%s: %d steps; the loop isn't killing anything", row.name, n)
				}
				break
			}
			// faultfs loads a hard link as two files, so the power loss of a second name is
			// TestMaterializeKeepsHardLinks's and the save table's row, not this loop's.
			if row.temp != "=" {
				e := setUpCrash(t, row)
				disk := e.kill(n)
				lost := filepath.Join(t.TempDir(), "vault")
				must(t, disk.Materialize(lost))
				e.dir = lost
				t.Run(row.name+"/power loss", func(t *testing.T) { e.checkRecovered(t, row, lost) })
			}
		}
	}
}

// setUpCrash sets up row's sides under its save intent.
func setUpCrash(t *testing.T, row crashRow) *env {
	t.Helper()
	e := newEnv(t)
	e.base = e.saveIntent()
	target := filepath.Join(e.dir, "notes", "a.md")
	e.write("notes/a.md", row.target)
	switch row.temp {
	case "":
	case "=":
		must(t, os.Link(target, filepath.Join(e.dir, e.temp())))
	default:
		e.write(e.temp(), row.temp)
	}
	_ = e.v.Close()
	return e
}

// kill runs a start's recovery on e.dir, killed at its vault step n (or not, when it has
// fewer steps), and returns the disk it ran on.
func (e *env) kill(n int) *faultfs.Disk {
	e.t.Helper()
	disk, err := faultfs.New(e.dir, n)
	must(e.t, err)
	gate := &recovering{Disk: disk}
	e.v, err = vault.Open(e.dir, fs.WithHook(gate)) // Open's probe is no step of recovery's
	must(e.t, err)
	gate.on = true
	_, err = Recover(ctx, Recovery{DB: e.db, Vault: e.v, State: e.state, Log: e.logger(), Audit: audit.New(e.logger())})
	_ = e.v.Close()
	if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
		e.t.Fatalf("kill at %d: %v", n, err)
	}
	return disk
}

// recovering passes the vault's steps to the Disk only once on: Open's rename probe leaves
// nothing, so the model need not see it.
type recovering struct {
	*faultfs.Disk
	on bool
}

func (g *recovering) Before(op fs.Op) error {
	if !g.on {
		return nil
	}
	return g.Disk.Before(op)
}

func (g *recovering) After(op fs.Op, err error) {
	if g.on {
		g.Disk.After(op, err)
	}
}

// checkRecovered starts on the vault at dir and checks the outcome: the intent ended, the
// note's bytes, its row (generation, hash, and after a roll forward the note's stat now),
// the temp gone and the copies.
func (e *env) checkRecovered(t *testing.T, row crashRow, dir string) {
	e.t = t
	var err error
	e.v, err = vault.Open(dir)
	must(t, err)
	t.Cleanup(func() { _ = e.v.Close() })
	e.recover()
	if _, open := e.intent(); open {
		t.Fatal("the intent is still open")
	}
	want, sum := e.base, e.base.SHA256
	if row.forward {
		s := sha256.Sum256([]byte("new"))
		want.Generation, sum = want.Generation+1, s[:]
	}
	got := e.row()
	if got.Generation != want.Generation || string(got.SHA256) != string(sum) {
		t.Fatalf("row %+v; want generation %d, hash %x", got, want.Generation, sum)
	}
	if b := e.read("notes/a.md"); b != row.target {
		t.Fatalf("a.md holds %q, want %q", b, row.target)
	}
	if row.forward {
		var st syscall.Stat_t
		must(t, syscall.Stat(filepath.Join(dir, "notes", "a.md"), &st))
		if got.Stat != (sqlstore.FileStat{Size: st.Size, Device: st.Dev, Inode: st.Ino, MtimeNS: st.Mtim.Nano(),
			CtimeNS: st.Ctim.Nano()}) {
			t.Fatalf("rolled forward with stat %+v; want a.md's now", got.Stat)
		}
	}
	if e.exists(e.temp()) {
		t.Fatal("the temp is still there")
	}
	if c := e.copies(); (row.kept && (len(c) != 1 || c["temp"] != "new")) || (!row.kept && len(c) != 0) {
		t.Fatalf("copies %v", c)
	}
}
