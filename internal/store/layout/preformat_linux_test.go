//go:build linux

package layout

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

const (
	stamp = "20261117T090000Z"
	tag   = "6f1c2a7e-3b4d-4c5e-8f90-a1b2c3d4e5f6 2026-11-17T09:00:00Z\n" // this upgrade's
)

// v01State is a v0.1 state directory: the format file and the credentials, no config.yaml.
func v01State(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	for name, c := range map[string]string{formatFile: "1\n", "auth.json": `{"hash":"x"}`, "secret.key": "key"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// copyOf makes an earlier pre-format-2/ in state: auth.json with content, and complete as
// the caller's func makes it (nil: none).
func copyOf(t *testing.T, state, content string, complete func(p string)) {
	t.Helper()
	dir := filepath.Join(state, PreFormat2)
	must(t, os.Mkdir(dir, 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte(content), 0o600))
	if complete != nil {
		complete(filepath.Join(dir, "complete"))
	}
}

// TestCopyPreFormat2 is ADR-0030's upgrade step 1 (mechanics in ADR-0041): each file present is
// copied 0600, then complete is written with the upgrade's tag; config.yaml is copied only
// when present.
func TestCopyPreFormat2(t *testing.T) {
	state := v01State(t)
	var steps []string
	kept, err := CopyPreFormat2(state, stamp, tag, func(s string) { steps = append(steps, s) })
	if err != nil || kept {
		t.Fatalf("CopyPreFormat2 = %v, %v", kept, err)
	}
	dir := filepath.Join(state, PreFormat2)
	if got, want := names(t, dir), []string{"auth.json", "complete", formatFile, "secret.key"}; !slices.Equal(got, want) {
		t.Fatalf("pre-format-2/ = %v, want %v", got, want)
	}
	for _, n := range []string{"auth.json", formatFile, "secret.key"} {
		a, _ := os.ReadFile(filepath.Join(state, n))
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil || string(a) != string(b) {
			t.Fatalf("%s copied as %q (%v), want %q", n, b, err, a)
		}
		fi, _ := os.Stat(filepath.Join(dir, n))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s is %v, want 0600", n, fi.Mode().Perm())
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "complete")); string(b) != tag {
		t.Fatalf("complete = %q, want the upgrade's tag", b)
	}
	if want := []string{"copied", "copied", "copied", "complete"}; !slices.Equal(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	// config.yaml, when present, is copied too.
	state = v01State(t)
	must(t, os.WriteFile(filepath.Join(state, "config.yaml"), []byte("a: b\n"), 0o600))
	if _, err := CopyPreFormat2(state, stamp, tag, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(names(t, filepath.Join(state, PreFormat2)), "config.yaml") {
		t.Fatal("config.yaml wasn't copied")
	}
}

// TestCopyPreFormat2KeepsThisUpgradesCopy: a resumed upgrade keeps the copy its own run
// completed, byte for byte.
func TestCopyPreFormat2KeepsThisUpgradesCopy(t *testing.T) {
	state := v01State(t)
	copyOf(t, state, "older", func(p string) { must(t, os.WriteFile(p, []byte(tag), 0o600)) })
	kept, err := CopyPreFormat2(state, stamp, tag, nil)
	if err != nil || !kept {
		t.Fatalf("CopyPreFormat2 = %v, %v; want kept", kept, err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, PreFormat2, "auth.json")); string(b) != "older" {
		t.Fatalf("this upgrade's copy was written over: %q", b)
	}
}

// TestCopyPreFormat2MovesAsideWhatIsntThisUpgradesCopy: a copy without complete, or whose
// complete isn't a regular file with this upgrade's tag (an earlier upgrade's, kept through
// a rollback and a password reset), is moved aside, never written over and never kept; a
// name already taken gets a suffix.
func TestCopyPreFormat2MovesAsideWhatIsntThisUpgradesCopy(t *testing.T) {
	for name, complete := range map[string]func(p string){
		"no complete": nil,
		"an earlier upgrade's": func(p string) {
			must(t, os.WriteFile(p, []byte("00000000-0000-4000-8000-000000000000 2026-01-01T00:00:00Z\n"), 0o600))
		},
		"v0.2 part A's empty complete": func(p string) { must(t, os.WriteFile(p, nil, 0o600)) },
		"complete is a folder":         func(p string) { must(t, os.Mkdir(p, 0o700)) },
		"complete is a symlink": func(p string) {
			target := filepath.Join(filepath.Dir(p), "elsewhere")
			must(t, os.WriteFile(target, []byte(tag), 0o600))
			must(t, os.Symlink(target, p))
		},
	} {
		t.Run(name, func(t *testing.T) {
			state := v01State(t)
			copyOf(t, state, "stale", complete)
			must(t, os.Mkdir(filepath.Join(state, PreFormat2+"-"+stamp), 0o700))
			var steps []string
			kept, err := CopyPreFormat2(state, stamp, tag, func(s string) { steps = append(steps, s) })
			if err != nil || kept {
				t.Fatalf("CopyPreFormat2 = %v, %v; want a new copy", kept, err)
			}
			aside := filepath.Join(state, PreFormat2+"-"+stamp+"-2")
			if b, err := os.ReadFile(filepath.Join(aside, "auth.json")); err != nil || string(b) != "stale" {
				t.Fatalf("the earlier copy isn't at %s: %q, %v", filepath.Base(aside), b, err)
			}
			if b, _ := os.ReadFile(filepath.Join(state, PreFormat2, "auth.json")); string(b) != `{"hash":"x"}` {
				t.Fatalf("pre-format-2/auth.json = %q, want today's", b)
			}
			if steps[0] != "moved-aside" {
				t.Fatalf("steps = %v", steps)
			}
		})
	}
}

// TestCopyPreFormat2Refuses what isn't its folder, or a credential that isn't a regular file,
// before changing anything: an incomplete copy isn't moved aside either.
func TestCopyPreFormat2Refuses(t *testing.T) {
	symlinkedAuth := func(s string) {
		must(t, os.Remove(filepath.Join(s, "auth.json")))
		must(t, os.Symlink(filepath.Join(t.TempDir(), "x"), filepath.Join(s, "auth.json")))
	}
	for name, setup := range map[string]func(state string){
		"folder is a symlink":    func(s string) { must(t, os.Symlink(t.TempDir(), filepath.Join(s, PreFormat2))) },
		"folder is a file":       func(s string) { must(t, os.WriteFile(filepath.Join(s, PreFormat2), nil, 0o600)) },
		"auth.json is a symlink": symlinkedAuth,
		"an incomplete copy, and auth.json a symlink": func(s string) {
			copyOf(t, s, "stale", nil)
			symlinkedAuth(s)
		},
	} {
		t.Run(name, func(t *testing.T) {
			state := v01State(t)
			setup(state)
			before := names(t, state)
			if _, err := CopyPreFormat2(state, stamp, tag, nil); err == nil {
				t.Fatal("CopyPreFormat2 accepted it")
			}
			if got := names(t, state); !slices.Equal(got, before) {
				t.Fatalf("a refusal changed the state directory: %v, was %v", got, before)
			}
		})
	}
	// The error says which, for a folder that isn't one.
	state := v01State(t)
	must(t, os.Symlink(t.TempDir(), filepath.Join(state, PreFormat2)))
	if _, err := CopyPreFormat2(state, stamp, tag, nil); !errors.Is(err, fs.ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
