//go:build linux

package intents

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

type crashRow struct {
	name          string
	target, temp  string // as in TestRecoverASave
	forward, kept bool   // the outcome: rolled forward; the temp copied
}

// TestRecoveryCrashEverywhere kills recovery itself at each of its vault steps, for a row of
// each action that writes, then runs it again from what the kill left (the process) and from
// what a power loss at that moment would leave (the disk). The second run ends where an
// uninterrupted one does: each action's steps are ordered so that every prefix of them is a
// state its table already handles, so recovery needs no intent of its own.
func TestRecoveryCrashEverywhere(t *testing.T) {
	for _, row := range []crashRow{
		{"drop", "base", "ne", false, false},
		{"drop keeping the temp", "other", "new", false, true},
		{"roll forward", "new", "", true, false},
		{"roll forward over a second name", "new", "=", true, false},
	} {
		for n := 0; ; n++ {
			e, before, disk := killRecovery(t, row, n)
			t.Run(row.name+"/process", func(t *testing.T) { e.checkRecovered(t, row, before, e.dir) })
			if !disk.Crashed() {
				if n < 2 {
					t.Fatalf("%s: %d steps; the loop isn't killing anything", row.name, n)
				}
				break
			}
			// faultfs loads a hard link as two files, so the power loss of a second name is
			// TestMaterializeKeepsHardLinks's and the save table's row, not this loop's.
			if row.temp != "=" {
				e, before, disk = killRecovery(t, row, n)
				lost := filepath.Join(t.TempDir(), "vault")
				must(t, disk.Materialize(lost))
				t.Run(row.name+"/power loss", func(t *testing.T) { e.checkRecovered(t, row, before, lost) })
			}
		}
	}
}

// killRecovery sets up row with its save intent and runs recovery, killed at vault step n.
func killRecovery(t *testing.T, row crashRow, n int) (*env, int64, *faultfs.Disk) {
	t.Helper()
	e := newEnv(t)
	before := e.saveIntent().Generation
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
	disk, err := faultfs.New(e.dir, n)
	must(t, err)
	gate := &recovering{Disk: disk}
	e.v, err = vault.Open(e.dir, fs.WithHook(gate)) // Open's probe is no step of recovery's
	must(t, err)
	gate.on = true
	_, err = Recover(ctx, Recovery{DB: e.db, Vault: e.v, State: e.state, Log: e.logger(), Audit: audit.New(e.logger())})
	_ = e.v.Close()
	if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
		t.Fatalf("%s, kill at %d: %v", row.name, n, err)
	}
	return e, before, disk
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

// checkRecovered restarts on the vault at dir and checks the second recovery's outcome.
func (e *env) checkRecovered(t *testing.T, row crashRow, before int64, dir string) {
	e.t, e.dir = t, dir
	var err error
	e.v, err = vault.Open(dir)
	must(t, err)
	t.Cleanup(func() { _ = e.v.Close() })
	e.recover()
	if _, open := e.intent(); open {
		t.Fatal("the intent is still open")
	}
	want := before
	if row.forward {
		want++
	}
	if got := e.row().Generation; got != want {
		t.Fatalf("generation %d, want %d", got, want)
	}
	if e.exists(e.temp()) {
		t.Fatal("the temp is still there")
	}
	if c := e.copies(); (row.kept && (len(c) != 1 || c["temp"] != "new")) || (!row.kept && len(c) != 0) {
		t.Fatalf("copies %v", c)
	}
}
