//go:build linux

package layout

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
)

func readFormat(t *testing.T, state string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, formatFile))
	if errors.Is(err, os.ErrNotExist) {
		return "<missing>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestEnsureFormat: a new data directory gets format 1, written durably; the same format
// starts; a newer one, or anything that isn't a format number, is refused and left as it is.
func TestEnsureFormat(t *testing.T) {
	t.Run("missing: written", func(t *testing.T) {
		state := t.TempDir()
		if err := os.WriteFile(filepath.Join(state, formatNew), []byte("half"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := EnsureFormat(state); err != nil {
			t.Fatal(err)
		}
		if got := readFormat(t, state); got != "1\n" {
			t.Fatalf("format = %q, want 1", got)
		}
		if _, err := os.Lstat(filepath.Join(state, formatNew)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a leftover %s is still there: %v", formatNew, err)
		}
		if fi, err := os.Stat(filepath.Join(state, formatFile)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("format file: %v, %v; want 0600", fi, err)
		}
	})
	for _, tc := range []struct {
		name, content, wantErr string
		want                   error
	}{
		{"equal", "1\n", "", nil},
		{"equal, no newline", "1", "", nil},
		{"newer", "2\n", "this data was written by a newer PostelMD (format 2); run that version or newer", ErrNewerFormat},
		{"much newer", "40\n", "(format 40)", ErrNewerFormat},
		{"empty", "", "Back up the data directory first. If only PostelMD 0.1 ever used it, delete that file", ErrBadFormat},
		{"text", "one\n", "damaged", ErrBadFormat},
		{"zero", "0\n", "damaged", ErrBadFormat},
		{"negative", "-1\n", "damaged", ErrBadFormat},
		{"signed", "+1\n", "damaged", ErrBadFormat},
		{"two lines", "1\n2\n", "damaged", ErrBadFormat},
		{"only whitespace", " \n", "damaged", ErrBadFormat},
		{"CRLF", "1\r\n", "damaged", ErrBadFormat},
		{"leading space", " 1\n", "damaged", ErrBadFormat},
		{"trailing space", "1 \n", "damaged", ErrBadFormat},
		{"two newlines", "1\n\n", "damaged", ErrBadFormat},
		{"huge", strings.Repeat("9", 40) + "\n", "damaged", ErrBadFormat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			if err := os.WriteFile(filepath.Join(state, formatFile), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := EnsureFormat(state)
			if !errors.Is(err, tc.want) || (tc.want != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("EnsureFormat with %q = %v, want %v containing %q", tc.content, err, tc.want, tc.wantErr)
			}
			if got := readFormat(t, state); got != tc.content {
				t.Fatalf("format changed to %q", got)
			}
		})
	}
	t.Run("a symlink to a valid format", func(t *testing.T) {
		// The target holds a valid format, so only the refusal to follow the link can fail it.
		state := t.TempDir()
		target := filepath.Join(t.TempDir(), "format")
		if err := os.WriteFile(target, []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(state, formatFile)); err != nil {
			t.Fatal(err)
		}
		if err := EnsureFormat(state); !errors.Is(err, fs.ErrSymlink) {
			t.Fatalf("EnsureFormat through a symlink = %v, want fs.ErrSymlink", err)
		}
	})
	t.Run("a directory", func(t *testing.T) {
		state := t.TempDir()
		if err := os.Mkdir(filepath.Join(state, formatFile), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := EnsureFormat(state); !errors.Is(err, fs.ErrNotRegular) {
			t.Fatalf("EnsureFormat on a directory = %v, want fs.ErrNotRegular", err)
		}
	})
}

// TestEnsureFormatSurvivesEveryCrashPoint: writing the format is a temp file, its fsync, one
// rename and the directory's fsync. A crash at any step, as a process kill or a power loss,
// leaves no format file or a whole one, never a torn one that the next start would refuse;
// and the next start finishes the job.
func TestEnsureFormatSurvivesEveryCrashPoint(t *testing.T) {
	for crashAt := 0; ; crashAt++ {
		state := t.TempDir()
		disk, err := faultfs.New(state, crashAt)
		if err != nil {
			t.Fatal(err)
		}
		err = EnsureFormat(state, fs.WithHook(disk))
		if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
			t.Fatalf("crash at step %d: %v", crashAt, err)
		}
		completed := !disk.Crashed()
		afterPowerLoss, ok := disk.AfterPowerLoss()[formatFile]
		for model, got := range map[string]string{"process kill": readFormat(t, state), "power loss": map[bool]string{true: string(afterPowerLoss), false: "<missing>"}[ok]} {
			if got != "1\n" && (completed || got != "<missing>") {
				t.Errorf("crash at step %d, after a %s: format = %q (completed %v)", crashAt, model, got, completed)
			}
		}
		if err := EnsureFormat(state); err != nil || readFormat(t, state) != "1\n" {
			t.Fatalf("crash at step %d: the next start = %v, format %q", crashAt, err, readFormat(t, state))
		}
		if completed {
			if crashAt < 3 {
				t.Fatalf("writing the format took %d steps; want the temp, its sync, the rename and the directory sync", crashAt)
			}
			return
		}
	}
}
