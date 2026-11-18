//go:build linux

package intents

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// TestResolveCrashEverywhere kills resolve at each of its vault steps, then runs it again from
// what the kill left (the process) and from what a power loss then would keep (the disk). It
// needs no intent of its own: the flagged intent ends only after the copy is made and the temp
// removed, durably, so every prefix is a flagged intent whose temp is there or gone, with its
// copy kept; the second resolve ends where an uninterrupted one does.
func TestResolveCrashEverywhere(t *testing.T) {
	for n := 0; ; n++ {
		for _, lost := range []bool{false, true} {
			e := flaggedWithTemp(t, "draft", "")
			_ = e.v.Close()
			disk, err := faultfs.New(e.dir, n)
			must(t, err)
			gate := &recovering{Disk: disk}
			e.v, err = vault.Open(e.dir, fs.WithHook(gate))
			must(t, err)
			gate.on = true
			_, err = Resolve(ctx, Recovery{DB: e.db, Vault: e.v, State: e.state, Log: e.logger(), Audit: audit.New(e.logger())}, opA)
			_ = e.v.Close()
			if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
				t.Fatalf("kill at %d: %v", n, err)
			}
			if !disk.Crashed() {
				if n < 2 {
					t.Fatalf("%d steps; the loop isn't killing anything", n)
				}
				return
			}
			if lost {
				e.dir = filepath.Join(t.TempDir(), "vault")
				must(t, disk.Materialize(e.dir))
			}
			e.v, err = vault.Open(e.dir)
			must(t, err)
			if _, open := e.intent(); !open {
				t.Fatalf("kill at %d (power loss %v): the intent ended before its temp went", n, lost)
			}
			_, err = e.resolve()
			must(t, err)
			_ = e.v.Close()
			if _, open := e.intent(); open || e.exists(e.temp()) || e.copies()["temp"] != "draft" || len(e.copies()) != 1 {
				t.Fatalf("kill at %d (power loss %v): intent open %v, temp %v, copies %v", n, lost, open, e.exists(e.temp()), e.copies())
			}
		}
	}
}
