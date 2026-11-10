//go:build linux

package layout

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPrepare(t *testing.T) {
	data := t.TempDir()
	p, _, err := Prepare(data, Options{})
	if err != nil {
		t.Skipf("the test's temp filesystem isn't on the allow-list: %v", err)
	}
	for _, d := range []string{p.State, p.Vault} {
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", d, st, err)
		}
	}
	if _, _, err := Prepare(data, Options{}); err != nil {
		t.Fatalf("Prepare is not idempotent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(data, "runbook.MD"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Prepare(data, Options{}); !errors.Is(err, ErrNotesAtRoot) {
		t.Fatalf("notes at the root = %v, want ErrNotesAtRoot", err)
	}
	if _, _, err := Prepare(filepath.Join(data, "missing"), Options{}); err == nil {
		t.Fatal("a missing data directory was accepted")
	}
}

func TestFilesystemDecisions(t *testing.T) {
	// /dev/shm is tmpfs on Linux: it starts, flagged as not persistent.
	if d, err := checkFS("/dev/shm", false); err != nil || d != Ephemeral {
		t.Fatalf("tmpfs = %v, %v; want Ephemeral", d, err)
	}
	// /proc's magic (0x9FA0) is on no list: refused unless overridden.
	if _, err := checkFS("/proc", false); err == nil {
		t.Fatal("an unknown filesystem was accepted")
	}
	if _, err := checkFS("/proc", true); err != nil {
		t.Fatalf("the override didn't apply: %v", err)
	}
}

// fakeFS presents a filesystem magic per directory (the test can't mount them): the state
// directory gets state, every other directory gets vault.
func fakeFS(t *testing.T, state, vault int64) {
	t.Helper()
	statfs = func(path string, st *unix.Statfs_t) error {
		magic := vault
		if filepath.Base(path) == ".postelmd" {
			magic = state
		}
		// Statfs_t.Type's integer type differs between architectures.
		reflect.ValueOf(&st.Type).Elem().SetInt(magic)
		return nil
	}
	t.Cleanup(func() { statfs = unix.Statfs })
}

// TestNetworkFilesystemsAreRefused presents each network filesystem's statfs magic: the
// directory is refused, naming the filesystem, unless the operator overrides it (ADR-0014
// §1). A folder shared from Windows or macOS (9p, virtiofs) gets the named-volume fix.
func TestNetworkFilesystemsAreRefused(t *testing.T) {
	for _, tt := range []struct {
		name      string
		magic     int64
		hostShare bool
	}{
		{"9p", 0x01021997, true},
		{"virtiofs", 0x6A656A63, true},
		{"fuse", 0x65735546, false},
		{"nfs", 0x6969, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fakeFS(t, tt.magic, tt.magic)
			_, err := checkFS("/data/vaults/notes", false)
			if err == nil || !strings.Contains(err.Error(), "is on "+tt.name+",") {
				t.Fatalf("%s = %v, want a refusal naming it", tt.name, err)
			}
			if got := strings.Contains(err.Error(), "-v postelmd-data:/data"); got != tt.hostShare {
				t.Fatalf("%s: named-volume fix in %q = %v, want %v", tt.name, err, got, tt.hostShare)
			}
			if d, err := checkFS("/data/vaults/notes", true); err != nil || d != Persistent {
				t.Fatalf("%s with the override = %v, %v; want Persistent", tt.name, d, err)
			}
		})
	}
}

// TestPrepareChecksStateAndVault: the vault can be a separate bind mount (an existing notes
// folder), so its filesystem is checked like the state directory's; the worse of the two
// decides. The container's own layer (no volume at /data) is refused unless the operator
// opts in; tmpfs starts, flagged.
func TestPrepareChecksStateAndVault(t *testing.T) {
	const ext4, tmpfs, overlay, p9, virtiofs = 0xEF53, 0x01021994, 0x794C7630, 0x01021997, 0x6A656A63
	for _, tt := range []struct {
		name         string
		state, vault int64
		opts         Options
		want         Durability
		wantErr      string // substring; "" for none
	}{
		{"both on ext4", ext4, ext4, Options{}, Persistent, ""},
		{"a Windows-path vault on 9p", ext4, p9, Options{}, 0, "notes is on 9p"},
		{"a macOS vault on virtiofs", ext4, virtiofs, Options{}, 0, "notes is on virtiofs"},
		{"state on 9p", p9, ext4, Options{}, 0, ".postelmd is on 9p"},
		{"a 9p vault, overridden", ext4, p9, Options{AllowNetworkFS: true}, Persistent, ""},
		{"no volume at /data", overlay, overlay, Options{}, 0, "no volume is mounted at /data"},
		{"only the vault mounted", overlay, ext4, Options{}, 0, "no volume is mounted at /data"},
		{"no volume, opted in", overlay, overlay, Options{AllowEphemeralData: true}, ContainerLayer, ""},
		{"the network override isn't the ephemeral one", overlay, overlay, Options{AllowNetworkFS: true}, 0, "no volume"},
		{"tmpfs starts, flagged", tmpfs, tmpfs, Options{}, Ephemeral, ""},
		{"a tmpfs vault flags the whole", ext4, tmpfs, Options{}, Ephemeral, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fakeFS(t, tt.state, tt.vault)
			_, got, err := Prepare(t.TempDir(), tt.opts)
			if tt.wantErr == "" {
				if err != nil || got != tt.want {
					t.Fatalf("= %v, %v; want %v", got, err, tt.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
	if !strings.Contains(ErrNoVolume.Error(), "-v ~/postelmd:/data") || !strings.Contains(ErrNoVolume.Error(), "POSTELMD_ALLOW_EPHEMERAL_DATA") {
		t.Fatalf("the no-volume error lacks the fix: %v", ErrNoVolume)
	}
}
