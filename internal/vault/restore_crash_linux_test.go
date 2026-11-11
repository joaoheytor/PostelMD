//go:build linux

package vault

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
)

// TestCrashDuringRestore: the recovery table of a restore from the trash (ADR-0024), at
// every step, for a process kill and a power loss, in both trash layouts. A kill leaves the
// item in exactly one place: still trashed (the restore didn't happen) or restored, with at
// most an empty stamp folder left in the trash, which the listing skips. A power loss can
// also leave it in both places, never in neither. After the restart, an item still in the
// trash is listed and restores; a restored one isn't listed.
func TestCrashDuringRestore(t *testing.T) {
	for _, tc := range []struct {
		layout, id, trashed string
		folder              bool // the trash entry is a stamp folder to remove afterwards
	}{
		{"new", stamp2, stamp2 + "/a.md", true},
		{"old", stamp2 + "-a.md", stamp2 + "-a.md", false},
	} {
		t.Run(tc.layout, func(t *testing.T) {
			sawEmpty := false
			for crashAt := 0; ; crashAt++ {
				dir := t.TempDir()
				// The marker: this trash is PostelMD's own. It also puts the temp folder in the
				// disk's snapshot, as in crashEverywhere.
				writeFiles(t, dir, map[string]string{TrashDir + "/" + tc.trashed: "A", TmpDir + "/" + ownerMarker: ""})
				must(t, os.MkdirAll(filepath.Join(dir, "notes"), 0o700))
				disk, err := faultfs.New(dir, crashAt)
				must(t, err)
				hook := &armed{disk: disk}
				v, err := Open(dir, fs.WithHook(hook))
				must(t, err)
				hook.on = true
				_, err = v.RestoreTrashed(ctx, tc.id, "notes", "")
				_ = v.Close()
				if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
					t.Fatalf("crash at step %d: %v", crashAt, err)
				}
				completed := !disk.Crashed()

				for _, model := range []struct {
					name      string
					files     map[string]string
					powerLoss bool
				}{
					{"kill", files(t, dir), false},
					{"power loss", powerLossFiles(disk.AfterPowerLoss()), true},
				} {
					restored, inPlace := model.files["notes/a.md"]
					inTrash, trashed := model.files[TrashDir+"/"+tc.trashed]
					both := inPlace && trashed && model.powerLoss && !completed
					if (inPlace == trashed && !both) || (inPlace && restored != "A") || (trashed && inTrash != "A") || (completed && !inPlace) {
						t.Fatalf("crash at step %d, %s: restored %q (%v), trashed %q (%v), completed %v",
							crashAt, model.name, restored, inPlace, inTrash, trashed, completed)
					}
				}

				// Kill: what is left in the trash is the item, an empty stamp folder, or nothing.
				_, stillTrashed := files(t, dir)[TrashDir+"/"+tc.trashed]
				entry, statErr := os.Lstat(filepath.Join(dir, TrashDir, tc.id))
				leftover := statErr == nil && !stillTrashed
				if leftover {
					if !tc.folder || !entry.IsDir() {
						t.Fatalf("crash at step %d: unexpected leftover %s", crashAt, entry.Name())
					}
					inside, err := os.ReadDir(filepath.Join(dir, TrashDir, tc.id))
					if err != nil || len(inside) != 0 {
						t.Fatalf("crash at step %d: the leftover holds %d entries (%v)", crashAt, len(inside), err)
					}
					sawEmpty = true
				}
				if completed && statErr == nil {
					t.Fatalf("completed, but the trash entry %s is still there", tc.id)
				}

				// After the restart: listed only while trashed, and restorable then.
				reopened, err := Open(dir)
				must(t, err)
				items, _, err := reopened.ListTrash(10)
				must(t, err)
				if (len(items) == 1) != stillTrashed || len(items) > 1 {
					t.Fatalf("crash at step %d: %d listed, still trashed %v", crashAt, len(items), stillTrashed)
				}
				if stillTrashed {
					if got, err := reopened.RestoreTrashed(ctx, tc.id, "notes", ""); err != nil || got != "notes/a.md" {
						t.Fatalf("crash at step %d: the retry = %q, %v", crashAt, got, err)
					}
				}
				_ = reopened.Close()
				if completed {
					break
				}
			}
			if tc.folder && !sawEmpty {
				t.Fatal("no crash point left an empty stamp folder: the steps changed, update this test")
			}
		})
	}
}
