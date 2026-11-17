//go:build linux

package layout

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCheckOwner: an offline command runs as the user who owns /data/.postelmd, or /data
// before .postelmd exists, so what it writes stays the server's (R6 of #190; ADR-0042). Any
// other effective UID, root included, is refused, naming the owner's UID and --user; a
// symlinked state directory is an error.
func TestCheckOwner(t *testing.T) {
	me := os.Geteuid()
	withState := t.TempDir()
	must(t, os.Mkdir(filepath.Join(withState, ".postelmd"), 0o700))
	for name, data := range map[string]string{"with .postelmd": withState, "before .postelmd": t.TempDir()} {
		if err := CheckOwner(data, me); err != nil {
			t.Fatalf("%s: CheckOwner as the owner = %v", name, err)
		}
		for _, euid := range []int{me + 1, 0} {
			if euid == me {
				continue // running as root: the owner is root
			}
			err := CheckOwner(data, euid)
			if !errors.Is(err, ErrNotOwner) {
				t.Fatalf("%s: CheckOwner as %d = %v, want ErrNotOwner", name, euid, err)
			}
			for _, want := range []string{"--user " + strconv.Itoa(me), "UID " + strconv.Itoa(euid)} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("%s: the refusal doesn't say %q: %v", name, want, err)
				}
			}
		}
	}
	linked := t.TempDir()
	must(t, os.Symlink(withState, filepath.Join(linked, ".postelmd")))
	if err := CheckOwner(linked, me); err == nil {
		t.Fatal("CheckOwner followed a symlinked state directory")
	}
}

// TestCheckOwnerFollowsASymlinkedDataDir: before .postelmd exists, the data directory may be a
// symlink to a folder (a host path given through a link); its target's owner decides. The
// state directory itself is never followed (TestCheckOwner).
func TestCheckOwnerFollowsASymlinkedDataDir(t *testing.T) {
	me := os.Geteuid()
	data := filepath.Join(t.TempDir(), "data")
	must(t, os.Symlink(t.TempDir(), data))
	if err := CheckOwner(data, me); err != nil {
		t.Fatalf("CheckOwner on a symlinked data directory, as its owner = %v", err)
	}
	if err := CheckOwner(data, me+1); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("CheckOwner on a symlinked data directory, as another user = %v, want ErrNotOwner", err)
	}
}
