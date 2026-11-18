//go:build linux

package intents

import (
	"context"
	"errors"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
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
	atCommit := "not committed"
	e.wrap = func(s Store) Store {
		return completing{s, func() { atCommit = string(disk.AfterPowerLoss()["notes/a.md"]) }}
	}
	if tot := e.recover(); tot.RolledForward != 1 {
		t.Fatalf("totals %+v, want one rolled forward", tot)
	}
	// Synced before the completion commits, not only by the time recovery returns.
	if atCommit != "new" {
		t.Fatalf("a power loss as the completion committed would leave a.md %q under a row at the new hash", atCommit)
	}
}

// completing runs at when a CompleteSave has committed.
type completing struct {
	Store
	at func()
}

func (c completing) CompleteSave(ctx context.Context, opID string, st sqlstore.FileStat) error {
	err := c.Store.CompleteSave(ctx, opID, st)
	if err == nil {
		c.at()
	}
	return err
}

// TestSameFileNeedsTheDevice: the same inode number on another device is another file.
func TestSameFileNeedsTheDevice(t *testing.T) {
	a := vault.Judged{Present: true, Regular: true, Stat: fs.Stat{Device: 1, Inode: 7}}
	b := a
	if !sameFile(a, b) {
		t.Fatal("one device and inode: not the same file")
	}
	b.Stat.Device = 2
	if sameFile(a, b) {
		t.Fatal("the same inode on another device counted as the same file")
	}
	b = a
	b.Regular = false
	if sameFile(a, b) {
		t.Fatal("a side that isn't a regular file counted as the same file")
	}
}
