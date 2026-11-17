//go:build linux

package layout

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestCheckFormatAtFormat2 is CheckFormat for a build that writes data format 2 (the format is
// injected, so this runs at every FormatVersion). A missing format file is v0.1 data, or a new
// data directory: it is format 1, and nothing is written, so the first upgrade still runs
// (its last step writes 2). 1 and 2 are read as they are; anything newer is refused.
func TestCheckFormatAtFormat2(t *testing.T) {
	for _, c := range []struct {
		content string // "" for no file
		want    int
		err     error
	}{
		{"", 1, nil},
		{"1\n", 1, nil},
		{"2\n", 2, nil},
		{"3\n", 0, ErrNewerFormat},
		{"x\n", 0, ErrBadFormat},
	} {
		state := t.TempDir()
		if c.content != "" {
			must(t, os.WriteFile(filepath.Join(state, formatFile), []byte(c.content), 0o600))
		}
		got, err := CheckFormat(state, 2)
		if got != c.want || !errors.Is(err, c.err) {
			t.Fatalf("CheckFormat(%q, 2) = %d, %v; want %d, %v", c.content, got, err, c.want, c.err)
		}
		if after := readFormat(t, state); c.content == "" && after != "<missing>" {
			t.Fatalf("CheckFormat at format 2 wrote the missing format file: %q", after)
		}
	}
}

// TestCheckFormatAtFormat1: a format-1 build writes 1 for a missing file, as EnsureFormat
// always has, and refuses 2 as newer.
func TestCheckFormatAtFormat1(t *testing.T) {
	state := t.TempDir()
	if n, err := CheckFormat(state, 1); n != 1 || err != nil || readFormat(t, state) != "1\n" {
		t.Fatalf("CheckFormat(missing, 1) = %d, %v; file %q", n, err, readFormat(t, state))
	}
	must(t, os.WriteFile(filepath.Join(state, formatFile), []byte("2\n"), 0o600))
	if _, err := CheckFormat(state, 1); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("CheckFormat(2, 1) = %v, want ErrNewerFormat", err)
	}
}

// TestSetFormatWritesFormat2: the first upgrade's last step writes 2 at any FormatVersion;
// a format newer than this code knows is refused.
func TestSetFormatWritesFormat2(t *testing.T) {
	state := t.TempDir()
	must(t, SetFormat(state, 2))
	if got := readFormat(t, state); got != "2\n" {
		t.Fatalf("format = %q", got)
	}
	if err := SetFormat(state, MaxFormat+1); err == nil {
		t.Fatal("SetFormat wrote a format this code doesn't know")
	}
}
