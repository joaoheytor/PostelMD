//go:build linux

package faultfs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
)

// open returns a root over a fresh vault holding a.md ("old") and an empty dir sub/.
func open(t *testing.T, crashAt int) (*fs.Root, *faultfs.Disk) {
	t.Helper()
	vault := t.TempDir()
	must(t, os.Mkdir(filepath.Join(vault, "sub"), 0o700))
	must(t, os.WriteFile(filepath.Join(vault, "a.md"), []byte("old"), 0o600))
	disk, err := faultfs.New(vault, crashAt)
	must(t, err)
	r, err := fs.OpenRoot(vault, fs.WithHook(disk))
	must(t, err)
	t.Cleanup(func() { _ = r.Close() })
	return r, disk
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestPowerLossRules(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, r *fs.Root)
		want map[string]string // path -> content; "path/" for a directory
	}{
		{
			name: "the snapshot is durable",
			run:  func(*testing.T, *fs.Root) {},
			want: map[string]string{"a.md": "old", "sub/": ""},
		},
		{
			name: "a synced file without a synced directory entry is lost",
			run:  func(t *testing.T, r *fs.Root) { must(t, r.CreateFile("b.md", []byte("new"), 0o600)) },
			want: map[string]string{"a.md": "old", "sub/": ""},
		},
		{
			name: "a synced file with a synced directory entry survives",
			run: func(t *testing.T, r *fs.Root) {
				must(t, r.CreateFile("sub/b.md", []byte("new"), 0o600))
				must(t, r.SyncDir("sub"))
			},
			want: map[string]string{"a.md": "old", "sub/": "", "sub/b.md": "new"},
		},
		{
			name: "a rename is undone until its directories are synced",
			run:  func(t *testing.T, r *fs.Root) { must(t, r.Rename("a.md", "sub/a.md")) },
			want: map[string]string{"a.md": "old", "sub/": ""},
		},
		{
			name: "syncing only the destination keeps the file in both places",
			run: func(t *testing.T, r *fs.Root) {
				must(t, r.Rename("a.md", "sub/a.md"))
				must(t, r.SyncDir("sub"))
			},
			want: map[string]string{"a.md": "old", "sub/": "", "sub/a.md": "old"},
		},
		{
			name: "a removal is undone until its directory is synced",
			run:  func(t *testing.T, r *fs.Root) { must(t, r.Remove("a.md")) },
			want: map[string]string{"a.md": "old", "sub/": ""},
		},
		{
			name: "a synced removal is durable",
			run: func(t *testing.T, r *fs.Root) {
				must(t, r.Remove("a.md"))
				must(t, r.SyncDir("."))
			},
			want: map[string]string{"sub/": ""},
		},
		{
			name: "a new directory is lost until its parent is synced",
			run:  func(t *testing.T, r *fs.Root) { must(t, r.Mkdir("new")) },
			want: map[string]string{"a.md": "old", "sub/": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, disk := open(t, faultfs.Never)
			tt.run(t, r)
			got := disk.AfterPowerLoss()
			if len(got) != len(tt.want) {
				t.Fatalf("after power loss: %v, want %v", keys(got), tt.want)
			}
			for path, content := range tt.want {
				if c, ok := got[path]; !ok || string(c) != content {
					t.Errorf("after power loss %s = %q (present %v), want %q", path, c, ok, content)
				}
			}
		})
	}
}

func TestCrashStopsEveryLaterStep(t *testing.T) {
	r, disk := open(t, 1) // step 0 is create, step 1 its sync
	err := r.CreateFile("b.md", []byte("new"), 0o600)
	if !errors.Is(err, faultfs.ErrCrashed) {
		t.Fatalf("CreateFile = %v, want ErrCrashed", err)
	}
	if err := r.SyncDir("."); !errors.Is(err, faultfs.ErrCrashed) {
		t.Fatalf("SyncDir after the crash = %v, want ErrCrashed", err)
	}
	if !disk.Crashed() || disk.Steps() != 2 {
		t.Fatalf("Crashed() = %v, Steps() = %d; want true, 2", disk.Crashed(), disk.Steps())
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
