//go:build linux

package vault

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// surveyVault opens a vault without newVault's notes/a.md.
func surveyVault(t *testing.T) (*Vault, string) {
	t.Helper()
	v, dir := newVault(t)
	must(t, os.Remove(filepath.Join(dir, "notes", "a.md")))
	return v, dir
}

func TestSurvey(t *testing.T) {
	v, dir := surveyVault(t)
	writeFiles(t, dir, map[string]string{
		"a.md":                  "A",
		"notes/b.md":            "BB",
		"notes/deep/c.txt":      "CCC",
		"notes/\xff.md":         "not UTF-8",
		".git/config":           "reserved",
		TrashDir + "/x/old.md":  "in the trash: imported as a trash record, not a file",
		"notes/.postelmd-tmp/x": "a reserved name below the root is reserved too",
		"empty.md":              "",
	})
	must(t, os.Symlink("a.md", filepath.Join(dir, "link.md")))
	got, err := v.Survey(ctx)
	must(t, err)
	want := map[string]string{"a.md": "A", "notes/b.md": "BB", "notes/deep/c.txt": "CCC", "notes/\xff.md": "not UTF-8", "empty.md": ""}
	if len(got) != len(want) {
		t.Fatalf("Survey found %d files, want %d: %+v", len(got), len(want), got)
	}
	for _, o := range got {
		content, ok := want[o.Path]
		if !ok {
			t.Fatalf("Survey listed %q", o.Path)
		}
		if s := sha256.Sum256([]byte(content)); o.SHA256 == nil || *o.SHA256 != s {
			t.Fatalf("%q: hash %x, want %x", o.Path, o.SHA256, s)
		}
		if st, err := v.root.Stat(o.Path); err != nil || st != o.Stat {
			t.Fatalf("%q: stat %+v, want %+v (%v)", o.Path, o.Stat, st, err)
		}
	}
}

// A file a writer keeps changing is hashed again (stat, hash, stat; ADR-0031), and after
// three tries recorded without a hash, for the scan to finish.
func TestSurveyBusyWriter(t *testing.T) {
	for _, c := range []struct {
		name    string
		writes  int // how many tries see a write between the hash and the second stat
		hashed  bool
		content string
	}{
		{"quiet", 0, true, "x"},
		{"busy twice", 2, true, "x++"},
		{"busy every time", 3, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			v, dir := surveyVault(t)
			writeFiles(t, dir, map[string]string{"n.md": "x"})
			tries := 0
			v.afterHash = func(p string) {
				if tries++; tries <= c.writes {
					f, err := os.OpenFile(filepath.Join(dir, p), os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // G304: the test's vault
					must(t, err)
					_, err = f.WriteString("+")
					must(t, errors.Join(err, f.Close()))
				}
			}
			got, err := v.Survey(ctx)
			must(t, err)
			if len(got) != 1 {
				t.Fatalf("Survey = %+v", got)
			}
			o := got[0]
			if !c.hashed {
				if o.SHA256 != nil || tries != 3 {
					t.Fatalf("after %d tries the hash is %x, want none after 3", tries, o.SHA256)
				}
				return
			}
			if s := sha256.Sum256([]byte(c.content)); o.SHA256 == nil || *o.SHA256 != s {
				t.Fatalf("hash %x, want the hash of %q", o.SHA256, c.content)
			}
			if st, _ := v.root.Stat("n.md"); st != o.Stat {
				t.Fatalf("stat %+v, want the last one %+v", o.Stat, st)
			}
		})
	}
}

// A file removed or replaced by a symlink during the walk isn't recorded, and isn't an
// error: the scan sees whatever is there later.
func TestSurveySkipsWhatGoesAway(t *testing.T) {
	v, dir := surveyVault(t)
	writeFiles(t, dir, map[string]string{"gone.md": "x", "linked.md": "y", "kept.md": "z"})
	v.afterHash = func(p string) {
		switch p {
		case "gone.md":
			must(t, os.Remove(filepath.Join(dir, p)))
		case "linked.md":
			must(t, os.Remove(filepath.Join(dir, p)))
			must(t, os.Symlink("kept.md", filepath.Join(dir, p)))
		}
	}
	got, err := v.Survey(ctx)
	must(t, err)
	if len(got) != 1 || got[0].Path != "kept.md" {
		t.Fatalf("Survey = %+v, want kept.md only", got)
	}
}

func TestSurveyStopsWhenCanceled(t *testing.T) {
	v, dir := surveyVault(t)
	writeFiles(t, dir, map[string]string{"a.md": "A"})
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := v.Survey(stopped); !errors.Is(err, context.Canceled) {
		t.Fatalf("Survey = %v, want context.Canceled", err)
	}
}
