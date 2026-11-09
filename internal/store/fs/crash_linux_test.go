//go:build linux

package fs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
)

const (
	tmpDir = ".postelmd-tmp" // ADR-0003 §5
	note   = "notes/a.md"
)

var oldContent, newContent = []byte("old content\n"), []byte("new content, longer\n")

// replace is the file-system half of a content save (ADR-0003 §5, step 4): write the temp
// file and sync it, rename it over the note, then sync both directories.
func replace(r *fs.Root, opID string) error {
	tmp := tmpDir + "/" + opID
	if err := r.CreateFile(tmp, newContent, 0o600); err != nil {
		return err
	}
	if err := r.Rename(tmp, note); err != nil {
		return err
	}
	if err := r.SyncDir("notes"); err != nil {
		return err
	}
	return r.SyncDir(tmpDir)
}

func newSaveVault(t *testing.T) string {
	t.Helper()
	vault := t.TempDir()
	for _, d := range []string{tmpDir, "notes"} {
		if err := os.Mkdir(filepath.Join(vault, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(vault, note), oldContent, 0o600); err != nil {
		t.Fatal(err)
	}
	return vault
}

// crashEverywhere runs seq once per crash point, plus once without a crash, wrapping the
// disk model with wrap. check gets the note's content after a process kill (from disk) and
// after a power loss (from the model), and whether the sequence completed.
func crashEverywhere(t *testing.T, seq func(*fs.Root) error, wrap func(*faultfs.Disk) fs.Hook,
	check func(t *testing.T, afterKill, afterPowerLoss []byte, completed bool)) {
	t.Helper()
	for crashAt := 0; ; crashAt++ {
		vault := newSaveVault(t)
		disk, err := faultfs.New(vault, crashAt)
		if err != nil {
			t.Fatal(err)
		}
		r, err := fs.OpenRoot(vault, fs.WithHook(wrap(disk)))
		if err != nil {
			t.Fatal(err)
		}
		err = seq(r)
		_ = r.Close()
		if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
			t.Fatalf("crash at step %d: unexpected error %v", crashAt, err)
		}
		afterKill, err := os.ReadFile(filepath.Join(vault, note))
		if err != nil {
			t.Fatalf("crash at step %d: after a process kill the note is unreadable: %v", crashAt, err)
		}
		t.Run("", func(t *testing.T) {
			check(t, afterKill, disk.AfterPowerLoss()[note], !disk.Crashed())
		})
		if !disk.Crashed() {
			return
		}
	}
}

func oldOrNew(t *testing.T, model string, got []byte, completed bool) {
	t.Helper()
	switch {
	case completed && string(got) != string(newContent):
		t.Errorf("after a %s, the completed save left %q, want the new content", model, got)
	case string(got) != string(oldContent) && string(got) != string(newContent):
		t.Errorf("after a %s, the note is %q: neither the old nor the new content", model, got)
	}
}

func TestReplaceSurvivesEveryCrashPoint(t *testing.T) {
	crashEverywhere(t, func(r *fs.Root) error { return replace(r, "op1") },
		func(d *faultfs.Disk) fs.Hook { return d },
		func(t *testing.T, afterKill, afterPowerLoss []byte, completed bool) {
			oldOrNew(t, "process kill", afterKill, completed)
			oldOrNew(t, "power loss", afterPowerLoss, completed)
		})
}

// forgetFileSync is a negative control: it models a store that never fsyncs file data.
type forgetFileSync struct{ *faultfs.Disk }

func (f forgetFileSync) After(op fs.Op, err error) {
	if op.Kind != fs.OpSyncFile {
		f.Disk.After(op, err)
	}
}

// The harness must catch a missing fsync: without the file sync, a power loss after the
// rename leaves an empty note, the classic torn save.
func TestHarnessCatchesMissingFileSync(t *testing.T) {
	torn := false
	crashEverywhere(t, func(r *fs.Root) error { return replace(r, "op1") },
		func(d *faultfs.Disk) fs.Hook { return forgetFileSync{d} },
		func(_ *testing.T, _, afterPowerLoss []byte, _ bool) {
			torn = torn || (string(afterPowerLoss) != string(oldContent) && string(afterPowerLoss) != string(newContent))
		})
	if !torn {
		t.Fatal("the power-loss model never produced a torn note without the file fsync")
	}
}

// Without syncing the note's directory, a save that reported success is lost on power loss.
func TestHarnessCatchesMissingDirSync(t *testing.T) {
	lost := false
	crashEverywhere(t, func(r *fs.Root) error {
		if err := r.CreateFile(tmpDir+"/op1", newContent, 0o600); err != nil {
			return err
		}
		return r.Rename(tmpDir+"/op1", note) // no SyncDir
	}, func(d *faultfs.Disk) fs.Hook { return d },
		func(_ *testing.T, _, afterPowerLoss []byte, completed bool) {
			lost = lost || (completed && string(afterPowerLoss) != string(newContent))
		})
	if !lost {
		t.Fatal("the power-loss model kept an unsynced rename")
	}
}
