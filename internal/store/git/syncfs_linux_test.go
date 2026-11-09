//go:build linux

package git

import (
	iofs "io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// recorder logs every step reported by the store.
type recorder struct{ ops []fs.Op }

func (r *recorder) Before(fs.Op) error        { return nil }
func (r *recorder) After(op fs.Op, err error) { r.ops = append(r.ops, op) }

func looseObjects(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	root := filepath.Join(dir, ".git", "objects")
	err := filepath.WalkDir(root, func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if !d.IsDir() && len(rel) == 41 && rel[2] == '/' { // xx/38 hex digits
			out["objects/"+rel] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestObjectsAreDurableBeforeTheRef checks ADR-0016's order: objects → ref. Every loose
// object a commit writes is fsynced (as its temp file) and its directory and objects/ are
// fsynced, all before SetBranch takes the branch lock.
func TestObjectsAreDurableBeforeTheRef(t *testing.T) {
	_, dir := newRepo(t)
	rec := &recorder{}
	g, err := Open(dir, fs.WithHook(rec))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	before := looseObjects(t, dir)
	c := commit(t, g, plumbing.ZeroHash, map[string][]byte{"notes/a.md": []byte("a"), "b.md": []byte("b")})
	if err := g.SetBranch("main", plumbing.ZeroHash, c); err != nil {
		t.Fatal(err)
	}

	lockAt := -1
	tempSyncs := 0
	syncedDirs := map[string]bool{}
	for i, op := range rec.ops {
		if op.Kind == fs.OpCreate && op.Path == "refs/heads/main.lock" {
			lockAt = i
			break
		}
		switch {
		case op.Kind == fs.OpSyncFile && strings.HasPrefix(op.Path, "objects/pack/tmp_obj_"):
			tempSyncs++
		case op.Kind == fs.OpSyncDir:
			syncedDirs[op.Path] = true
		}
	}
	if lockAt < 0 {
		t.Fatalf("SetBranch never created its lock; steps: %v", rec.ops)
	}
	created := 0
	for obj := range looseObjects(t, dir) {
		if before[obj] {
			continue
		}
		created++
		if d := filepath.Dir(obj); !syncedDirs[d] {
			t.Errorf("%s: directory %s not synced before the ref", obj, d)
		}
	}
	if created != 5 { // two blobs, two trees (root and notes/), one commit
		t.Fatalf("commit created %d loose objects, want 5", created)
	}
	if tempSyncs < created {
		t.Errorf("%d object temp files synced before the ref, want at least %d", tempSyncs, created)
	}
	if !syncedDirs["objects"] {
		t.Error("objects/ not synced before the ref (it gains new xx/ directories)")
	}
	gitCLI(t, dir, "fsck", "--strict")
}

func TestDirOf(t *testing.T) {
	for in, want := range map[string]string{
		"objects/ab/cdef": "objects/ab", "objects/ab": "objects", "objects": ".", "HEAD": ".",
		"/objects/ab/cdef": "objects/ab", "": ".",
	} {
		if got := dirOf(in); got != want {
			t.Errorf("dirOf(%q) = %q, want %q", in, got, want)
		}
	}
}
