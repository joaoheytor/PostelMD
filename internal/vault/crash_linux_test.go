//go:build linux

package vault

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
)

// armed forwards steps to the fault disk only once the test arms it, so Open's own steps
// (creating the temp directory) aren't crash points of the operation under test.
type armed struct {
	disk *faultfs.Disk
	on   bool
}

func (a *armed) Before(op fs.Op) error {
	if !a.on {
		return nil
	}
	return a.disk.Before(op)
}

func (a *armed) After(op fs.Op, err error) {
	if a.on {
		a.disk.After(op, err)
	}
}

// files lists the regular files under dir with their content, skipping the temp directory
// (whose leftovers the next Open removes).
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	fsys := os.DirFS(dir)
	err := iofs.WalkDir(fsys, ".", func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p == TmpDir {
			return iofs.SkipDir
		}
		if d.Type().IsRegular() {
			b, err := iofs.ReadFile(fsys, p)
			out[p] = string(b)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func powerLossFiles(m map[string][]byte) map[string]string {
	out := map[string]string{}
	for k, b := range m {
		if !strings.HasSuffix(k, "/") && !strings.HasPrefix(k, TmpDir+"/") {
			out[k] = string(b)
		}
	}
	return out
}

// crashEverywhere runs op on a fresh vault (notes/a.md = "A", empty archive/), crashing at
// every step in turn until op completes. check sees the files after a process kill and
// after a power loss (powerLoss), and whether op completed.
func crashEverywhere(t *testing.T, op func(v *Vault) error, check func(t *testing.T, files map[string]string, completed, powerLoss bool)) {
	t.Helper()
	for crashAt := 0; ; crashAt++ {
		dir := t.TempDir()
		must(t, os.MkdirAll(filepath.Join(dir, "notes"), 0o700))
		must(t, os.MkdirAll(filepath.Join(dir, "archive"), 0o700))
		must(t, os.MkdirAll(filepath.Join(dir, TmpDir), 0o700))
		must(t, os.WriteFile(filepath.Join(dir, "notes", "a.md"), []byte("A"), 0o600))
		disk, err := faultfs.New(dir, crashAt)
		must(t, err)
		hook := &armed{disk: disk}
		v, err := Open(dir, fs.WithHook(hook))
		must(t, err)
		hook.on = true
		err = op(v)
		_ = v.Close()
		if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
			t.Fatalf("crash at step %d: %v", crashAt, err)
		}
		completed := !disk.Crashed()
		t.Run("kill", func(t *testing.T) { check(t, files(t, dir), completed, false) })
		t.Run("power loss", func(t *testing.T) { check(t, powerLossFiles(disk.AfterPowerLoss()), completed, true) })

		reopened, err := Open(dir) // restart: the sweep runs
		must(t, err)
		_ = reopened.Close()
		if n := temps(dir); n != 0 {
			t.Fatalf("crash at step %d: %d temp files after a restart", crashAt, n)
		}
		if completed {
			return
		}
	}
}

// trashed returns the content of the single trashed copy of a.md
// (.postelmd-trash/<stamp>/a.md), or "" if none.
func trashed(t *testing.T, files map[string]string) string {
	t.Helper()
	var found []string
	for k, c := range files {
		if parts := strings.Split(k, "/"); len(parts) == 3 && parts[0] == TrashDir && parts[2] == "a.md" {
			found = append(found, c)
		}
	}
	if len(found) > 1 {
		t.Fatalf("%d trashed copies", len(found))
	}
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

func TestCrashDuringSave(t *testing.T) {
	crashEverywhere(t, func(v *Vault) error {
		_, err := v.Save(ctx, "notes/a.md", RevOf([]byte("A")), []byte("B"))
		return err
	}, func(t *testing.T, f map[string]string, completed, _ bool) {
		if got := f["notes/a.md"]; got != "B" && (completed || got != "A") {
			t.Fatalf("notes/a.md = %q (completed %v)", got, completed)
		}
	})
}

func TestCrashDuringCreate(t *testing.T) {
	crashEverywhere(t, func(v *Vault) error {
		_, err := v.Create(ctx, "notes/new.md", []byte("N"))
		return err
	}, func(t *testing.T, f map[string]string, completed, _ bool) {
		got, ok := f["notes/new.md"]
		if (ok && got != "N") || (completed && !ok) {
			t.Fatalf("notes/new.md = %q, present %v (completed %v)", got, ok, completed)
		}
	})
}

func TestCrashDuringMove(t *testing.T) {
	crashEverywhere(t, func(v *Vault) error {
		return v.Move(ctx, "notes/a.md", "archive/a.md")
	}, func(t *testing.T, f map[string]string, completed, powerLoss bool) {
		from, inFrom := f["notes/a.md"]
		to, inTo := f["archive/a.md"]
		// A rename is atomic for a process kill. After a power loss before the source
		// directory is synced, the note may be in both places, but never in neither.
		duplicated := inFrom && inTo && powerLoss && !completed
		if (inFrom == inTo && !duplicated) || (inFrom && from != "A") || (inTo && to != "A") || (completed && !inTo) {
			t.Fatalf("notes/a.md %q (%v), archive/a.md %q (%v), completed %v", from, inFrom, to, inTo, completed)
		}
	})
}

func TestCrashDuringDelete(t *testing.T) {
	crashEverywhere(t, func(v *Vault) error {
		_, err := v.Delete(ctx, "notes/a.md")
		return err
	}, func(t *testing.T, f map[string]string, completed, powerLoss bool) {
		orig, inPlace := f["notes/a.md"]
		inTrash := trashed(t, f)
		duplicated := inPlace && inTrash != "" && powerLoss && !completed // as in Move
		if (inPlace == (inTrash != "") && !duplicated) || (inPlace && orig != "A") || (!inPlace && inTrash != "A") || (completed && inPlace) {
			t.Fatalf("notes/a.md %q (%v), trash %q, completed %v", orig, inPlace, inTrash, completed)
		}
	})
}
