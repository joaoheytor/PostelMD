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

// TestCheckOwner: an offline command runs as the user who owns /data/.postelmd, so what it
// writes stays the server's (R6 of #190; ADR-0042). Any other effective UID, root included,
// is refused, naming the owner's UID and --user; a symlinked state directory is an error.
func TestCheckOwner(t *testing.T) {
	state := t.TempDir()
	me := os.Geteuid()
	if err := CheckOwner(state, me); err != nil {
		t.Fatalf("CheckOwner as the owner = %v", err)
	}
	for _, euid := range []int{me + 1, 0} {
		if euid == me {
			continue // running as root: the owner is root
		}
		err := CheckOwner(state, euid)
		if !errors.Is(err, ErrNotOwner) {
			t.Fatalf("CheckOwner as %d = %v, want ErrNotOwner", euid, err)
		}
		for _, want := range []string{"--user " + strconv.Itoa(me), "UID " + strconv.Itoa(euid)} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the refusal doesn't say %q: %v", want, err)
			}
		}
	}
	link := filepath.Join(t.TempDir(), "state")
	must(t, os.Symlink(state, link))
	if err := CheckOwner(link, me); err == nil {
		t.Fatal("CheckOwner followed a symlinked state directory")
	}
}
