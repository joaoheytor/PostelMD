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

const (
	uuidA = "6f1c2a7e-3b4d-4c5e-8f90-a1b2c3d4e5f6"
	uuidB = "00000000-0000-4000-8000-000000000000"
)

func markerPath(dir string) string { return filepath.Join(dir, TmpDir, ownerMarker) }

func TestReadMarker(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		dir := t.TempDir()
		if m, err := ReadMarker(dir); err != nil || m != (Marker{}) {
			t.Fatalf("ReadMarker = %+v, %v; want absent", m, err)
		}
	})
	t.Run("empty, as v0.1 writes it", func(t *testing.T) {
		v, dir := newVault(t)
		_ = v
		if m, err := ReadMarker(dir); err != nil || m != (Marker{Present: true}) {
			t.Fatalf("ReadMarker = %+v, %v; want present and empty", m, err)
		}
	})
	for _, content := range []string{
		"postelmd-vault 1\n" + uuidA + "\n2\n",
		"postelmd-vault 1\n" + uuidA + "\n2", // no final newline
	} {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{TmpDir + "/" + ownerMarker: content})
		if m, err := ReadMarker(dir); err != nil || m != (Marker{Present: true, VaultUUID: uuidA, Format: 2}) {
			t.Fatalf("ReadMarker(%q) = %+v, %v", content, m, err)
		}
	}
}

// Anything but ADR-0031's three lines is refused, never guessed at (ADR-0030's format
// table: "present but unparseable").
func TestReadMarkerRefusesWhatItCantParse(t *testing.T) {
	for name, content := range map[string]string{
		"another marker version": "postelmd-vault 2\n" + uuidA + "\n2\n",
		"a line missing":         "postelmd-vault 1\n" + uuidA + "\n",
		"a line too many":        "postelmd-vault 1\n" + uuidA + "\n2\nmore\n",
		"an upper-case UUID":     "postelmd-vault 1\n" + strings.ToUpper(uuidA) + "\n2\n",
		"not a UUID":             "postelmd-vault 1\n../../etc\n2\n",
		"format 0":               "postelmd-vault 1\n" + uuidA + "\n0\n",
		"format with a zero":     "postelmd-vault 1\n" + uuidA + "\n02\n",
		"format not a number":    "postelmd-vault 1\n" + uuidA + "\ntwo\n",
		"CRLF":                   "postelmd-vault 1\r\n" + uuidA + "\r\n2\r\n",
		"a blank line":           "\n",
		"two final newlines":     "postelmd-vault 1\n" + uuidA + "\n2\n\n",
		"too large":              strings.Repeat("x", 4096),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{TmpDir + "/" + ownerMarker: content})
			if m, err := ReadMarker(dir); !errors.Is(err, ErrBadMarker) {
				t.Fatalf("ReadMarker = %+v, %v; want ErrBadMarker", m, err)
			}
		})
	}
	for name, setup := range map[string]func(dir string) error{
		"a symlink": func(dir string) error {
			return os.Symlink("/etc/hostname", markerPath(dir))
		},
		"a folder": func(dir string) error { return os.Mkdir(markerPath(dir), 0o700) },
		"the temp folder a symlink": func(dir string) error {
			if err := os.Remove(filepath.Join(dir, TmpDir)); err != nil {
				return err
			}
			return os.Symlink(t.TempDir(), filepath.Join(dir, TmpDir))
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			must(t, os.Mkdir(filepath.Join(dir, TmpDir), 0o700))
			must(t, setup(dir))
			if m, err := ReadMarker(dir); !errors.Is(err, ErrBadMarker) {
				t.Fatalf("ReadMarker = %+v, %v; want ErrBadMarker", m, err)
			}
		})
	}
}

func TestWriteMarker(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T) string{
		"a folder never opened": func(t *testing.T) string { return t.TempDir() },
		"a v0.1 vault":          func(t *testing.T) string { _, dir := newVault(t); return dir },
		"a marker naming another UUID": func(t *testing.T) string {
			dir := t.TempDir()
			must(t, WriteMarker(dir, uuidB, 2))
			return dir
		},
		"a leftover temp": func(t *testing.T) string {
			_, dir := newVault(t)
			writeFiles(t, dir, map[string]string{TmpDir + "/" + markerTemp: "partial"})
			return dir
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := setup(t)
			must(t, WriteMarker(dir, uuidA, 2))
			if m, err := ReadMarker(dir); err != nil || m != (Marker{Present: true, VaultUUID: uuidA, Format: 2}) {
				t.Fatalf("ReadMarker = %+v, %v", m, err)
			}
			if fi, err := os.Lstat(markerPath(dir)); err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("the marker: %v, %v", fi, err)
			}
			if _, err := os.Lstat(filepath.Join(dir, TmpDir, markerTemp)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the temp is left: %v", err)
			}
			// v0.1's adoption reads the marker as present whatever it holds (ADR-0027), and
			// the startup sweep keeps it.
			v, err := Open(dir)
			must(t, err)
			_ = v.Close()
			if m, _ := ReadMarker(dir); m.VaultUUID != uuidA {
				t.Fatalf("opening the vault changed the marker: %+v", m)
			}
		})
	}
	dir := t.TempDir()
	for _, bad := range []struct {
		uuid   string
		format int
	}{{"nope", 2}, {strings.ToUpper(uuidA), 2}, {uuidA, 0}} {
		if err := WriteMarker(dir, bad.uuid, bad.format); err == nil {
			t.Errorf("WriteMarker(%q, %d) wrote a marker", bad.uuid, bad.format)
		}
	}
	if m, err := ReadMarker(dir); err != nil || m.Present {
		t.Fatalf("a refused WriteMarker left %+v, %v", m, err)
	}
}

// With no marker, WriteMarker adopts the folder as ADR-0027 does, refusal included: a
// reserved folder holding entries PostelMD didn't make is refused and nothing changes, or the
// next Open's sweep would delete the user's files from the temp folder.
func TestWriteMarkerRefusesUnownedReservedFolders(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"the temp folder":  {TmpDir + "/mine.txt": "the user's"},
		"the trash folder": {TrashDir + "/mine.txt": "the user's"},
		// No exception for a name like the marker's temp: the empty marker is durable before the
		// temp is made, so no crash leaves the temp without the marker.
		"the temp folder, holding only a file named like the marker's temp": {TmpDir + "/" + markerTemp: "the user's"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, files)
			if err := WriteMarker(dir, uuidA, 2); !errors.Is(err, ErrForeignReserved) {
				t.Fatalf("WriteMarker = %v, want ErrForeignReserved", err)
			}
			if _, err := os.Lstat(markerPath(dir)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a refused WriteMarker left a marker: %v", err)
			}
			for p, want := range files {
				if got := content(t, dir, p); got != want {
					t.Fatalf("%s: %q, want %q", p, got, want)
				}
			}
		})
	}
}

// A symlink at the marker's name, dangling or not, is refused like a folder: replacing it
// would mark a folder adoption never checked, and the next sweep would delete the user's
// files beside it. The link and the files stay as they were.
func TestWriteMarkerRefusesASymlinkedMarker(t *testing.T) {
	for name, target := range map[string]string{"dangling": "nowhere", "to a file": "mine.txt"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{TmpDir + "/mine.txt": "the user's"})
			must(t, os.Symlink(target, markerPath(dir)))
			err := WriteMarker(dir, uuidA, 2)
			if !errors.Is(err, ErrBadMarker) || !strings.Contains(err.Error(), "move it aside") {
				t.Fatalf("WriteMarker = %v, want ErrBadMarker naming the fix", err)
			}
			if got := content(t, dir, TmpDir+"/mine.txt"); got != "the user's" {
				t.Fatalf("the user's file: %q", got)
			}
			if got, err := os.Readlink(markerPath(dir)); err != nil || got != target {
				t.Fatalf("the symlink: %q, %v; want it untouched (%q)", got, err, target)
			}
		})
	}
}

// A marker that is a folder (or another special file) isn't overwritten: the error says so.
func TestWriteMarkerRefusesAMarkerThatIsAFolder(t *testing.T) {
	dir := t.TempDir()
	must(t, os.MkdirAll(markerPath(dir), 0o700))
	writeFiles(t, dir, map[string]string{TmpDir + "/" + ownerMarker + "/inside": "x"})
	err := WriteMarker(dir, uuidA, 2)
	if !errors.Is(err, ErrBadMarker) || !strings.Contains(err.Error(), "move it aside") {
		t.Fatalf("WriteMarker = %v, want ErrBadMarker naming the fix", err)
	}
	if got := content(t, dir, TmpDir+"/"+ownerMarker+"/inside"); got != "x" {
		t.Fatalf("the folder's content changed: %q", got)
	}
}

// TestWriteMarkerSurvivesEveryCrashPoint: WriteMarker crashed at every step, under the
// process-kill and power-loss models, from each state the upgrade, db rebuild or vault claim
// can find (CLAUDE.md rule 14). The marker reads as what was there before or as the new one,
// never anything else, and the vault still opens: a crash never leaves a temp folder that
// ADR-0027's adoption would take for the user's.
func TestWriteMarkerSurvivesEveryCrashPoint(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, dir string){
		"no temp folder": func(*testing.T, string) {},
		"an empty temp folder, no marker": func(t *testing.T, dir string) {
			must(t, os.Mkdir(filepath.Join(dir, TmpDir), 0o700))
		},
		"an empty marker": func(t *testing.T, dir string) {
			writeFiles(t, dir, map[string]string{TmpDir + "/" + ownerMarker: ""})
		},
		"another UUID": func(t *testing.T, dir string) {
			writeFiles(t, dir, map[string]string{TmpDir + "/" + ownerMarker: "postelmd-vault 1\n" + uuidB + "\n2\n"})
		},
		"an empty marker and a crash's temp": func(t *testing.T, dir string) {
			writeFiles(t, dir, map[string]string{TmpDir + "/" + ownerMarker: "", TmpDir + "/" + markerTemp: "postelmd-vault 1\n"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			for crashAt := 0; ; crashAt++ {
				dir := t.TempDir()
				writeFiles(t, dir, map[string]string{"a.md": "A"})
				setup(t, dir)
				before, err := ReadMarker(dir)
				must(t, err)
				disk, err := faultfs.New(dir, crashAt)
				must(t, err)
				err = WriteMarker(dir, uuidA, 2, fs.WithHook(disk))
				if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
					t.Fatalf("crash at step %d: %v", crashAt, err)
				}
				written := Marker{Present: true, VaultUUID: uuidA, Format: 2}
				powerLoss := disk.AfterPowerLoss()
				for model, d := range map[string]string{"process kill": dir, "power loss": materialize(t, powerLoss)} {
					m, err := ReadMarker(d)
					if err != nil {
						t.Fatalf("crash at step %d, after a %s: %v", crashAt, model, err)
					}
					okBefore := m == before || !before.Present && m == Marker{Present: true}
					if !okBefore && m != written {
						t.Fatalf("crash at step %d, after a %s: the marker reads %+v (before %+v)", crashAt, model, m, before)
					}
					if model == "power loss" && !disk.Crashed() && m != written {
						t.Fatalf("WriteMarker returned, but a power loss leaves %+v", m)
					}
					v, err := Open(d)
					if err != nil {
						t.Fatalf("crash at step %d, after a %s: the vault doesn't open: %v", crashAt, model, err)
					}
					_ = v.Close()
				}
				if !disk.Crashed() {
					return
				}
			}
		})
	}
}
