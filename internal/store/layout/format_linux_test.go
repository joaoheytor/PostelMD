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

// check is CheckFormat for this build.
func check(state string) error {
	_, err := CheckFormat(state, FormatVersion)
	return err
}

// TestCheckFormatRefusesWhatItCantRead: a newer format, or anything that isn't a format
// number, is refused and left as it is.
func TestCheckFormatRefusesWhatItCantRead(t *testing.T) {
	for _, tc := range []struct {
		name, content, wantErr string
		want                   error
	}{
		{"equal", "2\n", "", nil},
		{"equal, no newline", "2", "", nil},
		{"older", "1\n", "", nil},
		{"newer", "3\n", "this data was written by a newer PostelMD (format 3); run that version or newer", ErrNewerFormat},
		{"much newer", "40\n", "(format 40)", ErrNewerFormat},
		{"empty", "", "Back up the data directory first. If only PostelMD 0.1 ever used it, delete that file", ErrBadFormat},
		{"text", "two\n", "damaged", ErrBadFormat},
		{"zero", "0\n", "damaged", ErrBadFormat},
		{"negative", "-2\n", "damaged", ErrBadFormat},
		{"signed", "+2\n", "damaged", ErrBadFormat},
		{"two lines", "2\n3\n", "damaged", ErrBadFormat},
		{"only whitespace", " \n", "damaged", ErrBadFormat},
		{"CRLF", "2\r\n", "damaged", ErrBadFormat},
		{"leading space", " 2\n", "damaged", ErrBadFormat},
		{"trailing space", "2 \n", "damaged", ErrBadFormat},
		{"two newlines", "2\n\n", "damaged", ErrBadFormat},
		{"huge", strings.Repeat("9", 40) + "\n", "damaged", ErrBadFormat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			if err := os.WriteFile(filepath.Join(state, formatFile), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := check(state)
			if !errors.Is(err, tc.want) || (tc.want != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("CheckFormat with %q = %v, want %v containing %q", tc.content, err, tc.want, tc.wantErr)
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
		if err := os.WriteFile(target, []byte("2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(state, formatFile)); err != nil {
			t.Fatal(err)
		}
		if err := check(state); !errors.Is(err, fs.ErrSymlink) {
			t.Fatalf("CheckFormat through a symlink = %v, want fs.ErrSymlink", err)
		}
	})
	t.Run("a directory", func(t *testing.T) {
		state := t.TempDir()
		if err := os.Mkdir(filepath.Join(state, formatFile), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := check(state); !errors.Is(err, fs.ErrNotRegular) {
			t.Fatalf("CheckFormat on a directory = %v, want fs.ErrNotRegular", err)
		}
	})
}

// TestSetFormatSurvivesEveryCrashPoint: writing the format (the first upgrade's and db
// rebuild's last step) is a temp file, its fsync, one rename and the directory's fsync. A
// crash at any step, as a process kill or a power loss, leaves the old format or the new
// one, never a torn one that the next start would refuse; and running it again finishes.
func TestSetFormatSurvivesEveryCrashPoint(t *testing.T) {
	for crashAt := 0; ; crashAt++ {
		state := t.TempDir()
		if err := os.WriteFile(filepath.Join(state, formatFile), []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		disk, err := faultfs.New(state, crashAt)
		if err != nil {
			t.Fatal(err)
		}
		err = SetFormat(state, 2, fs.WithHook(disk))
		if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
			t.Fatalf("crash at step %d: %v", crashAt, err)
		}
		completed := !disk.Crashed()
		afterPowerLoss, ok := disk.AfterPowerLoss()[formatFile]
		for model, got := range map[string]string{"process kill": readFormat(t, state), "power loss": map[bool]string{true: string(afterPowerLoss), false: "1\n"}[ok]} {
			if got != "2\n" && (completed || got != "1\n") {
				t.Errorf("crash at step %d, after a %s: format = %q (completed %v)", crashAt, model, got, completed)
			}
		}
		if err := SetFormat(state, 2); err != nil || readFormat(t, state) != "2\n" {
			t.Fatalf("crash at step %d: running it again = %v, format %q", crashAt, err, readFormat(t, state))
		}
		if completed {
			if crashAt < 3 {
				t.Fatalf("writing the format took %d steps; want the temp, its sync, the rename and the directory sync", crashAt)
			}
			return
		}
	}
}
