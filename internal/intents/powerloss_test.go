//go:build linux

package intents

import (
	"errors"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

var errNoSync = errors.New("test: the folder's sync didn't run")

// noSync fails every folder sync while on: a save's rename stays in the page cache only, as a
// process killed between the rename and its syncs leaves it.
type noSync struct {
	*faultfs.Disk
	on bool
}

func (h *noSync) Before(op fs.Op) error {
	if h.on && op.Kind == fs.OpSyncDir {
		return errNoSync
	}
	return h.Disk.Before(op)
}

// TestARollForwardIsDurable: a save's rename that landed but wasn't synced is rolled forward
// only after its folders are synced (ADR-0033's roll forward), so a power loss after the
// completion can't take the note back to its base under a row that says new.
func TestARollForwardIsDurable(t *testing.T) {
	e := newEnv(t)
	e.saveIntent()
	_ = e.v.Close()
	disk, err := faultfs.New(e.dir, faultfs.Never)
	must(t, err)
	h := &noSync{Disk: disk, on: true}
	e.v, err = vault.Open(e.dir, fs.WithHook(h))
	must(t, err)
	if _, err := e.v.Save(ctx, "notes/a.md", vault.RevOf([]byte("base")), []byte("new")); !errors.Is(err, errNoSync) {
		t.Fatalf("the save's syncs: %v, want them refused", err)
	}
	h.on = false
	if tot := e.recover(); tot.RolledForward != 1 {
		t.Fatalf("totals %+v, want one rolled forward", tot)
	}
	if got := string(disk.AfterPowerLoss()["notes/a.md"]); got != "new" {
		t.Fatalf("after a power loss, a.md holds %q under a row at the new hash", got)
	}
}
