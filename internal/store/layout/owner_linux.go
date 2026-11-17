//go:build linux

package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrNotOwner: an offline command runs as another user than the one who owns the state
// directory. Nothing was changed.
var ErrNotOwner = errors.New("layout: this command must run as the user who owns /data/.postelmd " +
	"(or /data, before .postelmd exists)")

// CheckOwner refuses an offline command whose effective UID euid isn't the owner of the state
// directory, dataDir/.postelmd, or of dataDir itself while that doesn't exist yet (ADR-0042).
// It runs before Prepare, which creates the folders as euid. What the command creates (a new
// database, rebuild.intent, a moved folder) would belong to euid, and the server, running as
// the owner, couldn't open it. Root is no exception. A state directory that is a symlink or not
// a folder is refused too; a data directory that is a symlink is followed. It changes nothing.
func CheckOwner(dataDir string, euid int) error {
	dir := filepath.Join(dataDir, ".postelmd")
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		// The data directory may be a symlink to a folder: its target's owner decides.
		// .postelmd itself is never followed (Lstat above).
		dir = dataDir
		fi, err = os.Stat(dir)
	}
	if err != nil {
		return fmt.Errorf("layout: the owner of the data: %w", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok {
		return fmt.Errorf("layout: %s isn't a folder (%s)", dir, fi.Mode().Type())
	}
	if owner := int(st.Uid); owner != euid {
		return fmt.Errorf("%w: it belongs to UID %d, and this runs as UID %d. Run it as the owner, with the "+
			"server stopped: in a container, docker run --rm --user %d:%d with the same volumes (ADR-0042)",
			ErrNotOwner, owner, euid, owner, st.Gid)
	}
	return nil
}
