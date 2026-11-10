//go:build linux

package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

var ctx = context.Background()

// newVault opens a vault holding notes/a.md ("A").
func newVault(t *testing.T, opts ...fs.Option) (*Vault, string) {
	t.Helper()
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "notes"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "notes", "a.md"), []byte("A"), 0o600))
	v, err := Open(dir, opts...)
	must(t, err)
	t.Cleanup(func() { _ = v.Close() })
	return v, dir
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func content(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestReadAndSave(t *testing.T) {
	v, dir := newVault(t)
	b, rev, err := v.Read("notes/a.md")
	must(t, err)
	if string(b) != "A" || rev != RevOf([]byte("A")) {
		t.Fatalf("Read = %q, %s", b, rev)
	}
	next, err := v.Save(ctx, "notes/a.md", rev, []byte("B"))
	must(t, err)
	if next != RevOf([]byte("B")) || content(t, dir, "notes/a.md") != "B" {
		t.Fatalf("Save = %s; file %q", next, content(t, dir, "notes/a.md"))
	}
	same, err := v.Save(ctx, "notes/a.md", next, []byte("B"))
	if err != nil || same != next {
		t.Fatalf("saving identical content = %s, %v", same, err)
	}
}

func TestSaveConflictReturnsTheCurrentContent(t *testing.T) {
	v, dir := newVault(t)
	_, err := v.Save(ctx, "notes/a.md", RevOf([]byte("stale")), []byte("B"))
	var c *ConflictError
	if !errors.As(err, &c) || !errors.Is(err, ErrConflict) {
		t.Fatalf("Save with a stale rev = %v, want a ConflictError", err)
	}
	if string(c.Current) != "A" || c.Rev != RevOf([]byte("A")) {
		t.Fatalf("conflict carries %q, %s", c.Current, c.Rev)
	}
	if content(t, dir, "notes/a.md") != "A" {
		t.Fatal("a conflicting save changed the file")
	}
}

func TestSaveNeverCreates(t *testing.T) {
	v, dir := newVault(t)
	if _, err := v.Save(ctx, "notes/gone.md", RevOf([]byte("A")), []byte("B")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Save to a missing file = %v, want ErrNotFound", err)
	}
	if content(t, dir, "notes/gone.md") != "<missing>" {
		t.Fatal("Save created a file")
	}
	if _, err := v.Save(ctx, "notes", RevOf(nil), []byte("B")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Save to a directory = %v, want ErrNotFound", err)
	}
}

func TestCreate(t *testing.T) {
	v, dir := newVault(t)
	rev, err := v.Create(ctx, "notes/new.md", []byte("N"))
	must(t, err)
	if rev != RevOf([]byte("N")) || content(t, dir, "notes/new.md") != "N" {
		t.Fatalf("Create = %s, file %q", rev, content(t, dir, "notes/new.md"))
	}
	if _, err := v.Create(ctx, "notes/a.md", []byte("X")); !errors.Is(err, ErrExists) {
		t.Fatalf("Create over a file = %v, want ErrExists", err)
	}
	if content(t, dir, "notes/a.md") != "A" {
		t.Fatal("Create clobbered a file")
	}
	if _, err := v.Create(ctx, "missing/x.md", []byte("X")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Create in a missing folder = %v, want ErrNotFound", err)
	}
	assertNoTemps(t, dir)
}

func TestMkdirMoveAndDelete(t *testing.T) {
	v, dir := newVault(t)
	must(t, v.Mkdir(ctx, "archive"))
	if err := v.Mkdir(ctx, "archive"); !errors.Is(err, ErrExists) {
		t.Fatalf("second Mkdir = %v, want ErrExists", err)
	}
	must(t, v.Move(ctx, "notes/a.md", "archive/a.md"))
	if content(t, dir, "archive/a.md") != "A" || content(t, dir, "notes/a.md") != "<missing>" {
		t.Fatal("Move didn't move the file")
	}
	_, err := v.Create(ctx, "notes/a.md", []byte("other"))
	must(t, err)
	if err := v.Move(ctx, "notes/a.md", "archive/a.md"); !errors.Is(err, ErrExists) {
		t.Fatalf("Move onto a file = %v, want ErrExists", err)
	}
	if content(t, dir, "archive/a.md") != "A" {
		t.Fatal("Move clobbered its destination")
	}

	name, err := v.Delete(ctx, "archive/a.md")
	must(t, err)
	if content(t, dir, "archive/a.md") != "<missing>" || content(t, dir, TrashDir+"/"+name) != "A" {
		t.Fatalf("Delete didn't move the file to the trash (%s)", name)
	}
	folder, err := v.Delete(ctx, "notes")
	must(t, err)
	if content(t, dir, TrashDir+"/"+folder+"/a.md") != "other" {
		t.Fatal("deleting a folder didn't move it to the trash")
	}
	if _, err := v.Delete(ctx, "notes"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a missing path = %v, want ErrNotFound", err)
	}
}

func TestReservedPathsAreNotFound(t *testing.T) {
	v, dir := newVault(t)
	must(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]"), 0o600))
	for _, p := range []string{".git/config", ".GIT/config", TmpDir + "/x", TrashDir + "/x", "../escape", "/abs"} {
		checks := map[string]error{}
		_, _, checks["read"] = v.Read(p)
		_, checks["save"] = v.Save(ctx, p, RevOf([]byte("[core]")), []byte("x"))
		_, checks["create"] = v.Create(ctx, p, []byte("x"))
		checks["mkdir"] = v.Mkdir(ctx, p)
		checks["move from"] = v.Move(ctx, p, "notes/moved")
		checks["move to"] = v.Move(ctx, "notes/a.md", p)
		_, checks["delete"] = v.Delete(ctx, p)
		for op, err := range checks {
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("%s %q = %v, want ErrNotFound", op, p, err)
			}
		}
	}
	if content(t, dir, ".git/config") != "[core]" || content(t, dir, "notes/a.md") != "A" {
		t.Fatal("an operation on a reserved path changed something")
	}
}

func TestTooLarge(t *testing.T) {
	v, _ := newVault(t)
	big := make([]byte, MaxNoteSize+1)
	if _, err := v.Create(ctx, "big.md", big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Create = %v, want ErrTooLarge", err)
	}
	if _, err := v.Save(ctx, "notes/a.md", RevOf([]byte("A")), big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Save = %v, want ErrTooLarge", err)
	}
}

func TestOpenSweepsTempFiles(t *testing.T) {
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, TmpDir), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, TmpDir, "leftover"), []byte("half a save"), 0o600))
	v, err := Open(dir)
	must(t, err)
	t.Cleanup(func() { _ = v.Close() })
	assertNoTemps(t, dir)
}

func assertNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, TmpDir))
	must(t, err)
	if len(entries) != 0 {
		t.Fatalf("%d entries left in %s", len(entries), TmpDir)
	}
}

func TestConcurrentSavesWithTheSameRev(t *testing.T) {
	v, dir := newVault(t)
	base := RevOf([]byte("A"))
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = v.Save(ctx, "notes/a.md", base, []byte(strconv.Itoa(i)))
		}()
	}
	wg.Wait()
	won := -1
	for i, err := range errs {
		switch {
		case err == nil && won < 0:
			won = i
		case err == nil:
			t.Fatalf("saves %d and %d both succeeded with the same rev", won, i)
		case !errors.Is(err, ErrConflict):
			t.Fatalf("save %d: %v", i, err)
		}
	}
	if won < 0 || content(t, dir, "notes/a.md") != strconv.Itoa(won) {
		t.Fatalf("winner %d, file %q", won, content(t, dir, "notes/a.md"))
	}
}

// blockOn pauses the first step of kind on a vault path until release is closed, while
// the operation holds the vault mutex.
type blockOn struct {
	kind             fs.OpKind
	reached, release chan struct{}
	once             sync.Once
}

func newBlock(kind fs.OpKind) *blockOn {
	return &blockOn{kind: kind, reached: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockOn) Before(op fs.Op) error {
	if op.Kind == b.kind && !strings.HasPrefix(op.Path, TmpDir+"/") { // not Open's probe
		b.once.Do(func() { close(b.reached); <-b.release })
	}
	return nil
}
func (b *blockOn) After(fs.Op, error) {}

func TestSaveBehindADeleteNeverRecreates(t *testing.T) {
	block := newBlock(fs.OpRenameNoReplace)
	v, dir := newVault(t, fs.WithHook(block))
	deleted := make(chan error)
	go func() { _, err := v.Delete(ctx, "notes/a.md"); deleted <- err }()
	<-block.reached // the delete holds the mutex
	saved := make(chan error)
	go func() { _, err := v.Save(ctx, "notes/a.md", RevOf([]byte("A")), []byte("B")); saved <- err }()
	close(block.release)
	must(t, <-deleted)
	if err := <-saved; !errors.Is(err, ErrNotFound) {
		t.Fatalf("save after the delete = %v, want ErrNotFound", err)
	}
	if content(t, dir, "notes/a.md") != "<missing>" {
		t.Fatal("the save recreated the deleted note")
	}
}

func TestBusyAfterTheWait(t *testing.T) {
	block := newBlock(fs.OpRenameNoReplace)
	v, _ := newVault(t, fs.WithHook(block))
	v.wait = 20 * time.Millisecond
	done := make(chan error)
	go func() { _, err := v.Delete(ctx, "notes/a.md"); done <- err }()
	<-block.reached
	_, err := v.Create(ctx, "b.md", []byte("b"))
	close(block.release)
	must(t, <-done)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Create while the vault is held = %v, want ErrBusy", err)
	}
}

func TestList(t *testing.T) {
	v, dir := newVault(t)
	must(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "b.md"), []byte("b"), 0o600))
	must(t, os.Symlink("b.md", filepath.Join(dir, "link.md")))
	must(t, os.WriteFile(filepath.Join(dir, "x.sync-conflict-1.md"), nil, 0o600))
	_, err := v.Delete(ctx, "b.md")
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, "b.md"), []byte("b"), 0o600))

	got, err := v.List("")
	must(t, err)
	want := []Entry{{Name: "b.md", Kind: KindFile}, {Name: "link.md", Kind: KindOther}, {Name: "notes", Kind: KindFolder}}
	if !slices.Equal(got, want) {
		t.Fatalf("List(root) = %v, want %v (reserved entries hidden)", got, want)
	}
	sub, err := v.List("notes")
	must(t, err)
	if !slices.Equal(sub, []Entry{{Name: "a.md", Kind: KindFile}}) {
		t.Fatalf("List(notes) = %v", sub)
	}
	for _, p := range []string{"missing", "notes/a.md", ".git", "link.md"} {
		if _, err := v.List(p); !errors.Is(err, ErrNotFound) {
			t.Errorf("List(%q) = %v, want ErrNotFound", p, err)
		}
	}
	if _, _, err := v.Read("link.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Read through a symlink = %v, want ErrNotFound (never followed)", err)
	}
}

// noReplace9p fails every RENAME_NOREPLACE with EINVAL, as a Docker Desktop Windows-path
// bind mount (9p) does.
type noReplace9p struct{}

func (noReplace9p) Before(op fs.Op) error {
	if op.Kind == fs.OpRenameNoReplace {
		return syscall.EINVAL // the same Errno the store checks for
	}
	return nil
}
func (noReplace9p) After(fs.Op, error) {}

func TestOpenRefusesAFilesystemWithoutNoReplace(t *testing.T) {
	dir := t.TempDir()
	_, err := Open(dir, fs.WithHook(noReplace9p{}))
	if !errors.Is(err, fs.ErrNoReplaceUnsupported) || !strings.Contains(err.Error(), "named volume") {
		t.Fatalf("Open on a 9p-like filesystem = %v, want ErrNoReplaceUnsupported with the fix", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, TmpDir)); len(entries) != 0 {
		t.Fatalf("the probe left %d files", len(entries))
	}
}
