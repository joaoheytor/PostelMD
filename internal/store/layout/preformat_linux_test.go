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

const stamp = "20261117T090000Z"

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

// TestCopyPreFormat2 is ADR-0030's upgrade step 1 (mechanics in ADR-0041): each file present is
// copied 0600, then complete is written; config.yaml is copied only when present.
func TestCopyPreFormat2(t *testing.T) {
	state := v01State(t)
	var steps []string
	kept, err := CopyPreFormat2(state, stamp, func(s string) { steps = append(steps, s) })
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
	if want := []string{"copied", "copied", "copied", "complete"}; !slices.Equal(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	// config.yaml, when present, is copied too.
	state = v01State(t)
	if err := os.WriteFile(filepath.Join(state, "config.yaml"), []byte("a: b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyPreFormat2(state, stamp, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(names(t, filepath.Join(state, PreFormat2)), "config.yaml") {
		t.Fatal("config.yaml wasn't copied")
	}
}

// TestCopyPreFormat2KeepsACompleteCopy: a resumed upgrade keeps it, byte for byte.
func TestCopyPreFormat2KeepsACompleteCopy(t *testing.T) {
	state := v01State(t)
	dir := filepath.Join(state, PreFormat2)
	must(t, os.Mkdir(dir, 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte("older"), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "complete"), nil, 0o600))
	kept, err := CopyPreFormat2(state, stamp, nil)
	if err != nil || !kept {
		t.Fatalf("CopyPreFormat2 = %v, %v; want kept", kept, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "auth.json")); string(b) != "older" {
		t.Fatalf("a complete copy was written over: %q", b)
	}
}

// TestCopyPreFormat2MovesAnIncompleteCopyAside: it is never written over; a name already
// taken gets a suffix.
func TestCopyPreFormat2MovesAnIncompleteCopyAside(t *testing.T) {
	state := v01State(t)
	dir := filepath.Join(state, PreFormat2)
	must(t, os.Mkdir(dir, 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte("half"), 0o600))
	must(t, os.Mkdir(filepath.Join(state, PreFormat2+"-"+stamp), 0o700))
	var steps []string
	if _, err := CopyPreFormat2(state, stamp, func(s string) { steps = append(steps, s) }); err != nil {
		t.Fatal(err)
	}
	aside := filepath.Join(state, PreFormat2+"-"+stamp+"-2")
	if b, err := os.ReadFile(filepath.Join(aside, "auth.json")); err != nil || string(b) != "half" {
		t.Fatalf("the incomplete copy isn't at %s: %q, %v", filepath.Base(aside), b, err)
	}
	if !slices.Contains(names(t, dir), "complete") || steps[0] != "moved-aside" {
		t.Fatalf("no new complete copy (steps %v)", steps)
	}
}

// TestCopyPreFormat2Refuses what isn't its folder, or a credential that isn't a regular file,
// before writing anything.
func TestCopyPreFormat2Refuses(t *testing.T) {
	for name, setup := range map[string]func(state string){
		"folder is a symlink": func(s string) { must(t, os.Symlink(t.TempDir(), filepath.Join(s, PreFormat2))) },
		"folder is a file":    func(s string) { must(t, os.WriteFile(filepath.Join(s, PreFormat2), nil, 0o600)) },
		"auth.json is a symlink": func(s string) {
			must(t, os.Remove(filepath.Join(s, "auth.json")))
			must(t, os.Symlink(filepath.Join(t.TempDir(), "x"), filepath.Join(s, "auth.json")))
		},
	} {
		t.Run(name, func(t *testing.T) {
			state := v01State(t)
			setup(state)
			before := names(t, state)
			if _, err := CopyPreFormat2(state, stamp, nil); err == nil {
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
	if _, err := CopyPreFormat2(state, stamp, nil); !errors.Is(err, fs.ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
