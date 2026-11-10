//go:build linux

// Package layout prepares the /data volume for the MVP (ADR-0020, architecture §3): app
// state in /data/.postelmd, one vault at /data/vaults/notes. It checks the state
// filesystem by statfs magic (ADR-0014 §1) and refuses notes at the root of /data
// (ADR-0009).
package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Paths under the data directory.
type Paths struct {
	State string // /data/.postelmd
	Vault string // /data/vaults/notes
	Lock  string // /data/.postelmd/instance.lock
}

// Durability of the state filesystem.
type Durability int

const (
	Persistent Durability = iota
	Ephemeral             // tmpfs or the container's own layer: works, but data is lost
)

// Filesystem magic numbers (ADR-0014 §1).
var (
	accepted = map[int64]string{0xEF53: "ext4", 0x58465342: "xfs", 0x9123683E: "btrfs", 0x2FC12FC1: "zfs",
		0xF2F52010: "f2fs", 0xCA451A4E: "bcachefs"}
	ephemeral = map[int64]string{0x01021994: "tmpfs", 0x794C7630: "overlayfs"}
	network   = map[int64]string{0x6969: "nfs", 0x517B: "smb", 0xFF534D42: "cifs", 0xFE534D42: "smb2",
		0x01021997: "9p", 0x65735546: "fuse", 0x00C36400: "ceph"}
)

// ErrNotesAtRoot: notes were found directly in /data instead of in a vault mount.
var ErrNotesAtRoot = errors.New("layout: Markdown files found directly in the data directory; " +
	"mount your notes folder at /data/vaults/notes instead (ADR-0009)")

// Prepare creates the state directory and the vault directory if missing, and checks the
// state filesystem. allowNetworkFS overrides a refusal (POSTELMD_ALLOW_NETWORK_FS; unsafe).
func Prepare(dataDir string, allowNetworkFS bool) (Paths, Durability, error) {
	p := Paths{
		State: filepath.Join(dataDir, ".postelmd"),
		Vault: filepath.Join(dataDir, "vaults", "notes"),
	}
	p.Lock = filepath.Join(p.State, "instance.lock")
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return p, 0, fmt.Errorf("layout: the data directory %s isn't readable: %w", dataDir, err)
	}
	for _, e := range entries {
		if strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			return p, 0, ErrNotesAtRoot
		}
	}
	for _, d := range []string{p.State, filepath.Dir(p.Vault), p.Vault} {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return p, 0, fmt.Errorf("layout: create %s: %w (is the data directory writable by this user?)", d, err)
		}
	}
	dur, err := checkStateFS(p.State, allowNetworkFS)
	return p, dur, err
}

func checkStateFS(dir string, allowNetworkFS bool) (Durability, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("layout: statfs %s: %w", dir, err)
	}
	magic := int64(st.Type) //nolint:unconvert // Statfs_t.Type is int64 on amd64 but not everywhere
	switch {
	case accepted[magic] != "":
		return Persistent, nil
	case ephemeral[magic] != "":
		return Ephemeral, nil
	case allowNetworkFS:
		return Persistent, nil
	case network[magic] != "":
		return 0, fmt.Errorf("layout: %s is on %s, which can't hold the app state safely (ADR-0014); "+
			"use a local volume, or set POSTELMD_ALLOW_NETWORK_FS=true at your own risk", dir, network[magic])
	default:
		return 0, fmt.Errorf("layout: %s is on an unknown filesystem (magic 0x%X), refused (ADR-0014)", dir, magic)
	}
}
