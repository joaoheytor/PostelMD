//go:build linux

package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrDatabaseOnNetworkFS: the state directory is on a filesystem only POSTELMD_ALLOW_NETWORK_FS
// accepts, and the database can't live there (ADR-0030).
var ErrDatabaseOnNetworkFS = errors.New("layout: /data/.postelmd is on a filesystem that only " +
	"POSTELMD_ALLOW_NETWORK_FS accepts, and PostelMD's database (.postelmd/app.db) can't live there: " +
	"SQLite needs working shared memory and POSIX locks (ADR-0030). With the server stopped, " +
	"copy the old .postelmd into a folder on a local disk that the server's user owns (your own " +
	"folder with --user, or a volume chowned to 65532, the image's user), and mount it at " +
	"/data/.postelmd; the notes can stay where they are, and the override still applies to them. " +
	`Steps: docs/operations/running-v0.1.md, "State folder on a network filesystem"`)

// checkDatabaseFS refuses a state directory whose filesystem only the network override
// accepted, when the command opens the database (it must never create one that later starts
// would refuse) or a database already exists there (the override no longer covers the state
// directory). It reads the filesystem with the override off, and never changes anything.
func checkDatabaseFS(state string, opts Options) error {
	if !opts.AllowNetworkFS {
		return nil // checkFS already refused anything the override would accept
	}
	if _, err := checkFS(state, false); err == nil {
		return nil
	}
	if opts.OpensDatabase {
		return ErrDatabaseOnNetworkFS
	}
	_, err := os.Lstat(filepath.Join(state, "app.db"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("layout: the database file: %w", err)
	default:
		return ErrDatabaseOnNetworkFS
	}
}
