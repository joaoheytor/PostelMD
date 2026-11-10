//go:build linux

// Package layout prepares the /data volume for the MVP (ADR-0020, architecture §3): app
// state in /data/.postelmd, one vault at /data/vaults/notes. It checks the filesystems of
// both by statfs magic (ADR-0014 §1), refuses the container's own layer (no volume at
// /data), and refuses notes at the root of /data (ADR-0009).
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

// Durability of the data directory, from best to worst.
type Durability int

const (
	Persistent     Durability = iota
	Ephemeral                 // tmpfs: works, but data is lost at the next reboot or docker rm
	ContainerLayer            // overlayfs, the container's own layer: lost at the next docker rm
)

// Options are the operator's overrides (internal/config).
type Options struct {
	AllowNetworkFS     bool // POSTELMD_ALLOW_NETWORK_FS: start on a refused filesystem; unsafe
	AllowEphemeralData bool // POSTELMD_ALLOW_EPHEMERAL_DATA: start without a volume at /data
}

// Filesystem magic numbers (ADR-0014 §1).
var (
	accepted = map[int64]string{0xEF53: "ext4", 0x58465342: "xfs", 0x9123683E: "btrfs", 0x2FC12FC1: "zfs",
		0xF2F52010: "f2fs", 0xCA451A4E: "bcachefs"}
	ephemeral = map[int64]string{0x01021994: "tmpfs"}
	container = map[int64]string{0x794C7630: "overlayfs"}
	network   = map[int64]string{0x6969: "nfs", 0x517B: "smb", 0xFF534D42: "cifs", 0xFE534D42: "smb2",
		0x01021997: "9p", 0x6A656A63: "virtiofs", 0x65735546: "fuse", 0x00C36400: "ceph"}
	// hostShares are how Docker Desktop shares a Windows or macOS folder (ADR-0014 §1).
	hostShares = map[int64]bool{0x01021997: true, 0x6A656A63: true}
)

// ErrNotesAtRoot: notes were found directly in /data instead of in a vault mount.
var ErrNotesAtRoot = errors.New("layout: Markdown files found directly in the data directory; " +
	"mount your notes folder at /data/vaults/notes instead (ADR-0009)")

// ErrNoVolume: /data is the container's own layer, so everything is lost at the next
// docker rm, which every upgrade does.
var ErrNoVolume = errors.New("layout: no volume is mounted at /data, so your notes would be lost " +
	"when the container is removed (every upgrade removes it). Create a folder and mount it: " +
	`mkdir -p ~/postelmd, then add -v ~/postelmd:/data --user "$(id -u):$(id -g)" to docker run. ` +
	"To try PostelMD without keeping anything, set POSTELMD_ALLOW_EPHEMERAL_DATA=true")

// Prepare creates the state directory and the vault directory if missing, and checks the
// filesystem of both: the vault can be a separate bind mount.
func Prepare(dataDir string, opts Options) (Paths, Durability, error) {
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
	worst := Persistent
	for _, d := range []string{p.State, p.Vault} {
		dur, err := checkFS(d, opts.AllowNetworkFS)
		if err != nil {
			return p, 0, err
		}
		worst = max(worst, dur)
	}
	if worst == ContainerLayer && !opts.AllowEphemeralData {
		return p, 0, ErrNoVolume
	}
	return p, worst, nil
}

// statfs is unix.Statfs; tests replace it to present filesystems they can't mount.
var statfs = unix.Statfs

func checkFS(dir string, allowNetworkFS bool) (Durability, error) {
	var st unix.Statfs_t
	if err := statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("layout: statfs %s: %w", dir, err)
	}
	magic := int64(st.Type) //nolint:unconvert // Statfs_t.Type is int64 on amd64 but not everywhere
	switch {
	case accepted[magic] != "":
		return Persistent, nil
	case ephemeral[magic] != "":
		return Ephemeral, nil
	case container[magic] != "":
		return ContainerLayer, nil
	case allowNetworkFS:
		return Persistent, nil
	case hostShares[magic]:
		return 0, fmt.Errorf("layout: %s is on %s, a folder shared from Windows or macOS, which can't hold "+
			"PostelMD's data safely (ADR-0014). Keep the folder on the Linux filesystem (in WSL, under ~, "+
			"never /mnt/c), or keep the notes in a named volume: -v postelmd-data:/data, without a notes "+
			"bind mount (ADR-0009)", dir, network[magic])
	case network[magic] != "":
		return 0, fmt.Errorf("layout: %s is on %s, which can't hold PostelMD's data safely (ADR-0014); "+
			"use a local volume, or set POSTELMD_ALLOW_NETWORK_FS=true at your own risk", dir, network[magic])
	default:
		return 0, fmt.Errorf("layout: %s is on an unknown filesystem (magic 0x%X), refused (ADR-0014)", dir, magic)
	}
}
