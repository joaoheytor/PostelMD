//go:build linux

package layout

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrNotOwner: an offline command runs as another user than the one who owns the state
// directory. Nothing was changed.
var ErrNotOwner = errors.New("layout: this command must run as the user who owns /data/.postelmd")

// CheckOwner refuses an offline command whose effective UID euid isn't the owner of the state
// directory (ADR-0042): the files it creates (a new database, rebuild.intent, a moved folder)
// would belong to euid, and the server, running as the owner, couldn't open them. Root is no
// exception. A state directory that is a symlink or not a folder is refused too. It changes
// nothing.
func CheckOwner(state string, euid int) error {
	fi, err := os.Lstat(state)
	if err != nil {
		return fmt.Errorf("layout: the state directory: %w", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok {
		return fmt.Errorf("layout: the state directory isn't a folder (%s)", fi.Mode().Type())
	}
	if owner := int(st.Uid); owner != euid {
		return fmt.Errorf("%w: it belongs to UID %d, and this runs as UID %d. Run it as the owner, with the "+
			"server stopped: in a container, docker run --rm --user %d:%d with the same volumes (ADR-0042)",
			ErrNotOwner, owner, euid, owner, st.Gid)
	}
	return nil
}
