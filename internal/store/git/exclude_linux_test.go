//go:build linux

package git

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
)

const reserved = "/.postelmd-trash/\n/.postelmd-tmp/\n"

// TestEnsureExclude adds the reserved folders to info/exclude once, keeping every other line.
func TestEnsureExclude(t *testing.T) {
	for _, tc := range []struct {
		name string
		old  *string // nil: no info/exclude, and no info directory
		want string
	}{
		{"missing", nil, reserved},
		{"empty", ptr(""), reserved},
		{"user lines", ptr("# mine\n*.secret\n"), "# mine\n*.secret\n" + reserved},
		{"no final newline", ptr("*.secret"), "*.secret\n" + reserved},
		{"one already there", ptr("/.postelmd-trash/\nx\n"), "/.postelmd-trash/\nx\n/.postelmd-tmp/\n"},
		{"both there", ptr("a\n" + reserved + "b"), "a\n" + reserved + "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, dir := newRepo(t)
			info := filepath.Join(dir, ".git", "info")
			if err := os.RemoveAll(info); err != nil {
				t.Fatal(err)
			}
			if tc.old != nil {
				if err := os.Mkdir(info, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(info, "exclude"), []byte(*tc.old), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 { // idempotent
				if err := g.EnsureExclude(); err != nil {
					t.Fatal(err)
				}
			}
			if b, err := g.Exclude(); err != nil || string(b) != tc.want {
				t.Fatalf("info/exclude = %q, %v; want %q", b, err, tc.want)
			}
		})
	}
}

// TestEnsureExcludeSurvivesEveryCrashPoint: after a kill or a power loss at any step,
// info/exclude has the user's lines, with or without the reserved ones, and the next
// EnsureExclude completes it.
func TestEnsureExcludeSurvivesEveryCrashPoint(t *testing.T) {
	const old = "# mine\n"
	for crashAt := 0; ; crashAt++ {
		_, dir := newRepo(t)
		path := filepath.Join(dir, ".git", "info", "exclude")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
			t.Fatal(err)
		}
		disk, err := faultfs.New(filepath.Join(dir, ".git"), crashAt)
		if err != nil {
			t.Fatal(err)
		}
		hooked, err := Open(dir, fs.WithHook(disk))
		if err != nil {
			t.Fatal(err)
		}
		err = hooked.EnsureExclude()
		_ = hooked.Close()
		if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
			t.Fatalf("crash at step %d: %v", crashAt, err)
		}
		completed := !disk.Crashed()
		afterKill, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("crash at step %d: info/exclude unreadable: %v", crashAt, err)
		}
		for model, got := range map[string][]byte{"process kill": afterKill, "power loss": disk.AfterPowerLoss()["info/exclude"]} {
			if want := map[string]bool{old: !completed, old + reserved: true}; !want[string(got)] {
				t.Errorf("crash at step %d, after a %s: info/exclude = %q (completed %v)", crashAt, model, got, completed)
			}
		}
		g, err := Open(dir) // restart
		if err != nil {
			t.Fatal(err)
		}
		if err := g.EnsureExclude(); err != nil {
			t.Fatalf("crash at step %d: the retry failed: %v", crashAt, err)
		}
		b, _ := g.Exclude()
		_ = g.Close()
		if string(b) != old+reserved {
			t.Fatalf("crash at step %d: after the retry info/exclude = %q", crashAt, b)
		}
		if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
			t.Fatalf("crash at step %d: leftovers in .git/info: %v", crashAt, entries)
		}
		if completed {
			return
		}
	}
}

// TestEnsureExcludeKeepsTheMode: the rewritten info/exclude keeps the user's permissions.
func TestEnsureExcludeKeepsTheMode(t *testing.T) {
	g, dir := newRepo(t)
	path := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := g.EnsureExclude(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o400 {
		t.Fatalf("mode = %v, %v; want 0400", fi.Mode(), err)
	}
}

// TestExcludeNotARegularFile: a symlinked or special info/exclude is never followed or
// rewritten; Exclude reports ErrExcludeNotRegular so the caller can warn and go on.
func TestExcludeNotARegularFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(path string) error
	}{
		{"symlink", func(p string) error { return os.Symlink("/dev/null", p) }},
		{"fifo", func(p string) error { return syscall.Mkfifo(p, 0o600) }},
		{"directory", func(p string) error { return os.Mkdir(p, 0o700) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, dir := newRepo(t)
			path := filepath.Join(dir, ".git", "info", "exclude")
			_ = os.RemoveAll(path)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := tc.make(path); err != nil {
				t.Fatal(err)
			}
			if b, err := g.Exclude(); !errors.Is(err, ErrExcludeNotRegular) || b != nil {
				t.Fatalf("Exclude = %q, %v; want ErrExcludeNotRegular", b, err)
			}
			if err := g.EnsureExclude(); !errors.Is(err, ErrExcludeNotRegular) {
				t.Fatalf("EnsureExclude = %v; want ErrExcludeNotRegular", err)
			}
			if fi, err := os.Lstat(path); err != nil || fi.Mode().IsRegular() {
				t.Fatalf("info/exclude was replaced: %v, %v", fi, err)
			}
		})
	}
}

func ptr(s string) *string { return &s }
