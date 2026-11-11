//go:build linux

package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
)

// materialize writes a power-loss model (faultfs.Disk.AfterPowerLoss) to a new directory.
func materialize(t *testing.T, m map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for p, b := range m {
		if strings.HasSuffix(p, "/") {
			must(t, os.MkdirAll(filepath.Join(dir, p), 0o700))
			continue
		}
		must(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o700))
		must(t, os.WriteFile(filepath.Join(dir, p), b, 0o600))
	}
	return dir
}

// TestAdoptionSurvivesEveryCrashPoint: the first start of an unmarked folder (the temp
// folder's mkdir, the marker's create, their syncs), then a delete into the trash, crashed at
// every step under the process-kill and power-loss models (CLAUDE.md rule 14). The next start
// always succeeds: PostelMD never refuses folders it made itself. Once the first start
// returned, the marker is durable, so a trash a later delete filled can't be taken for the
// user's.
func TestAdoptionSurvivesEveryCrashPoint(t *testing.T) {
	for crashAt := 0; ; crashAt++ {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"notes/a.md": "A"})
		disk, err := faultfs.New(dir, crashAt)
		must(t, err)
		opened := false
		v, err := Open(dir, fs.WithHook(disk))
		if err == nil {
			opened = true
			_, err = v.Delete(ctx, "notes/a.md")
			_ = v.Close()
		}
		if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
			t.Fatalf("crash at step %d: %v", crashAt, err)
		}
		powerLoss := disk.AfterPowerLoss()
		if _, marked := powerLoss[TmpDir+"/"+ownerMarker]; opened && !marked {
			t.Errorf("crash at step %d: Open returned, but a power loss loses the marker", crashAt)
		}
		for model, d := range map[string]string{"process kill": dir, "power loss": materialize(t, powerLoss)} {
			next, err := Open(d)
			if err != nil {
				t.Errorf("crash at step %d, after a %s: the next start = %v", crashAt, model, err)
				continue
			}
			_ = next.Close()
		}
		if !disk.Crashed() {
			return
		}
	}
}
