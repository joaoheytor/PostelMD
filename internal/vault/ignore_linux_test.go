//go:build linux

package vault

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func paths(cs []Change) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Path)
	}
	return out
}

// ignoredTree is a vault whose .gitignore files ignore everything but the paths in
// keptPaths, following git's rules: patterns at any depth, nested files scoped to their
// directory, negation and directory-only patterns.
var ignoredTree = map[string]string{
	".gitignore":     ".env\nsecrets/\n*.log\n!keep.log\n# a comment\n\n",
	".env":           "TOKEN=x",
	"secrets/k.md":   "k",
	"x.log":          "log",
	"deep/z.log":     "log",
	"keep.log":       "kept",
	"secrets.md":     "not a directory",
	"sub/.gitignore": "local.md\r\n",
	"sub/local.md":   "ignored here",
	"sub/n.md":       "n",
	"local.md":       "outside sub's scope",
}

var keptPaths = []string{".gitignore", "keep.log", "local.md", "notes/a.md", "secrets.md", "sub/.gitignore", "sub/n.md"}

// TestCommitSkipsIgnoredFiles: Status and the initial import leave out what .gitignore and
// .git/info/exclude ignore, in an adopted repository and in a plain folder alike, and the
// count ImportInitial reports is what it committed. The host's git agrees afterwards.
func TestCommitSkipsIgnoredFiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		adopted bool
		want    []string
	}{
		{"adopted repository", true, keptPaths},
		{"plain folder", false, keptPaths},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, dir := newVault(t)
			writeFiles(t, dir, ignoredTree)
			if tc.adopted {
				gitCLI(t, dir, "init", "-q", "-b", "main")
				writeFiles(t, dir, map[string]string{".git/info/exclude": "kube.yaml\n", "kube.yaml": "k"})
			}
			created, err := v.EnableGit()
			must(t, err)
			if created == tc.adopted {
				t.Fatalf("EnableGit created = %v", created)
			}
			if got := paths(changes(t, v)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("status = %v, want %v", got, tc.want)
			}
			if n, err := v.PendingImport(ctx); err != nil || n != len(tc.want) {
				t.Fatalf("PendingImport = %d, %v", n, err)
			}
			n, err := v.ImportInitial(ctx, "Ada", "ada@example.invalid")
			must(t, err)
			committed := strings.Split(gitCLI(t, dir, "ls-tree", "-r", "--name-only", "HEAD"), "\n")
			if !reflect.DeepEqual(committed, tc.want) || n != len(committed) {
				t.Fatalf("ImportInitial = %d, committed %v, want %v", n, committed, tc.want)
			}
			if got := gitCLI(t, dir, "status", "--porcelain", "--untracked-files=all"); got != "" {
				t.Fatalf("the host's git sees changes PostelMD didn't commit: %q", got)
			}
		})
	}
}

// TestIgnoredButTrackedFilesStayTracked: a pattern doesn't untrack a committed file (git's
// rule), so its modifications and its deletion are still committed, also inside an ignored
// directory; new files next to it stay out.
func TestIgnoredButTrackedFilesStayTracked(t *testing.T) {
	v, dir := newVault(t)
	writeFiles(t, dir, map[string]string{"t.log": "1", "dir/x.md": "1", "gone.log": "1"})
	_, err := v.EnableGit()
	must(t, err)
	_, err = v.ImportInitial(ctx, "Ada", "ada@example.invalid")
	must(t, err)

	writeFiles(t, dir, map[string]string{
		".gitignore": "*.log\ndir/\n", "t.log": "2", "dir/x.md": "2", "dir/new.md": "new", "new.log": "new",
	})
	must(t, os.Remove(filepath.Join(dir, "gone.log")))
	want := []Change{{Path: ".gitignore", Kind: Added}, {Path: "dir/x.md", Kind: Modified}, {Path: "gone.log", Kind: Deleted}, {Path: "t.log", Kind: Modified}}
	if got := changes(t, v); !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %v, want %v", got, want)
	}
	_, err = commitAll(v, "edit ignored but tracked files", "Ada", "ada@example.invalid")
	must(t, err)
	if got := gitCLI(t, dir, "ls-tree", "-r", "--name-only", "HEAD"); got != ".gitignore\ndir/x.md\nnotes/a.md\nt.log" {
		t.Fatalf("committed files = %q", got)
	}
	if got := gitCLI(t, dir, "show", "HEAD:t.log") + gitCLI(t, dir, "show", "HEAD:dir/x.md"); got != "22" {
		t.Fatalf("the tracked files' new content wasn't committed: %q", got)
	}
}

// TestIgnoredDirectoryIsNotWalked: an ignored directory such as node_modules/ is never
// listed. Its subdirectory can't be read, so walking into it fails the status.
func TestIgnoredDirectoryIsNotWalked(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable directories")
	}
	v, dir := newVault(t)
	files := map[string]string{".gitignore": "node_modules/\n"}
	for i := range 200 {
		files["node_modules/pkg/f"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+".js"] = "x"
	}
	writeFiles(t, dir, files)
	locked := filepath.Join(dir, "node_modules", "locked")
	must(t, os.Mkdir(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // removing a directory needs its x bit
	_, err := v.EnableGit()
	must(t, err)
	if got := paths(changes(t, v)); !reflect.DeepEqual(got, []string{".gitignore", "notes/a.md"}) {
		t.Fatalf("status = %v", got)
	}
}

// TestUnwritableOrOddExcludeDoesntStopGit: adopting a repository whose .git/info can't be
// written, or whose info/exclude is a symlink or a FIFO, still starts: EnableGit succeeds
// and a warning names only the repository-relative file. A read-only info/exclude still
// applies, so Status works. One PostelMD can't read safely fails Status and Commit closed
// (git would follow the symlink and apply its patterns; PostelMD won't leave the vault).
func TestUnwritableOrOddExcludeDoesntStopGit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, info string)
		root   bool // needs a non-root user
		unsafe bool // Status and Commit fail with ErrUnsafeExclude
	}{
		{"read-only info", func(t *testing.T, info string) {
			must(t, os.Chmod(info, 0o500))                  //nolint:gosec // read-only, but still listable
			t.Cleanup(func() { _ = os.Chmod(info, 0o700) }) //nolint:gosec // removing a directory needs its x bit
		}, true, false},
		{"symlinked exclude", func(t *testing.T, info string) {
			must(t, os.WriteFile(filepath.Join(info, "elsewhere"), []byte("a.md\n"), 0o600))
			must(t, os.Remove(filepath.Join(info, "exclude")))
			must(t, os.Symlink("elsewhere", filepath.Join(info, "exclude")))
		}, false, true},
		{"FIFO exclude", func(t *testing.T, info string) {
			must(t, os.Remove(filepath.Join(info, "exclude")))
			must(t, syscall.Mkfifo(filepath.Join(info, "exclude"), 0o600))
		}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.root && os.Geteuid() == 0 {
				t.Skip("root writes read-only directories")
			}
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"a.md": "A"})
			gitCLI(t, dir, "init", "-q", "-b", "main")
			info := filepath.Join(dir, ".git", "info")
			writeFiles(t, dir, map[string]string{".git/info/exclude": "# mine\n"})
			tc.setup(t, info)
			var logs bytes.Buffer
			v, err := Open(dir)
			must(t, err)
			defer func() { _ = v.Close() }()
			v.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
			_, err = v.EnableGit()
			must(t, err)
			if tc.unsafe {
				if _, err := v.Status(ctx); !errors.Is(err, ErrUnsafeExclude) {
					t.Fatalf("Status = %v, want ErrUnsafeExclude", err)
				}
				if _, err := commitAll(v, "x", "Ada", "a@b"); !errors.Is(err, ErrUnsafeExclude) {
					t.Fatalf("Commit = %v, want ErrUnsafeExclude", err)
				}
				if got := gitCLI(t, dir, "rev-list", "--all"); got != "" {
					t.Fatalf("something was committed: %q", got)
				}
			} else if got := paths(changes(t, v)); !reflect.DeepEqual(got, []string{"a.md"}) {
				t.Fatalf("status = %v", got)
			}
			out := logs.String()
			if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "info/exclude") || strings.Contains(out, dir) {
				t.Fatalf("want a warning naming only info/exclude, got %q", out)
			}
		})
	}
}

// TestAdoptedRepositoryExcludesTheTrash: adopting a repository adds PostelMD's reserved
// folders to .git/info/exclude once, keeping the user's lines, so a host `git add -A`
// never commits the trash.
func TestAdoptedRepositoryExcludesTheTrash(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.md": "A", "b.md": "B"})
	gitCLI(t, dir, "init", "-q", "-b", "main")
	writeFiles(t, dir, map[string]string{".git/info/exclude": "# mine\n*.secret"})
	for range 2 { // a restart adopts it again
		v, err := Open(dir)
		must(t, err)
		_, err = v.EnableGit()
		must(t, err)
		_ = v.Close()
	}
	b, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	must(t, err)
	if want := "# mine\n*.secret\n/.postelmd-trash/\n/.postelmd-tmp/\n"; string(b) != want {
		t.Fatalf("info/exclude = %q, want %q", b, want)
	}
	v, err := Open(dir)
	must(t, err)
	defer func() { _ = v.Close() }()
	_, err = v.EnableGit()
	must(t, err)
	_, err = v.Delete(ctx, "b.md")
	must(t, err)
	gitCLI(t, dir, "add", "-A")
	if got := gitCLI(t, dir, "diff", "--cached", "--name-only"); got != "a.md" {
		t.Fatalf("git add -A staged %q, want only a.md", got)
	}
}
