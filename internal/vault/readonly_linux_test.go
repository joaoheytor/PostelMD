//go:build linux

package vault

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A read-only vault (ADR-0037) refuses every mutation with ErrReadOnly and changes nothing on
// disk; reads still work.
func TestAReadOnlyVaultRefusesEveryChange(t *testing.T) {
	v, dir := newVault(t)
	if _, err := v.EnableGit(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ImportInitial(ctx, "N", "n@example.com"); err != nil {
		t.Fatal(err)
	}
	versions, err := v.History("notes/a.md", 1)
	if err != nil || len(versions) != 1 {
		t.Fatalf("history: %v, %v", versions, err)
	}
	_, rev, err := v.Read("notes/a.md")
	must(t, err)
	if v.ReadOnly() || !v.SetReadOnly() || v.SetReadOnly() || !v.ReadOnly() {
		t.Fatal("SetReadOnly: want false, then true once, then read-only")
	}
	changes := map[string]func() error{
		"save":          func() error { _, err := v.Save(ctx, "notes/a.md", rev, []byte("B")); return err },
		"save, same":    func() error { _, err := v.Save(ctx, "notes/a.md", rev, []byte("A")); return err },
		"create":        func() error { _, err := v.Create(ctx, "new.md", []byte("N")); return err },
		"mkdir":         func() error { return v.Mkdir(ctx, "folder") },
		"move":          func() error { return v.Move(ctx, "notes/a.md", "b.md") },
		"delete":        func() error { _, err := v.Delete(ctx, "notes/a.md"); return err },
		"restore trash": func() error { _, err := v.RestoreTrashed(ctx, "20240102T030405.000000000Z", "", ""); return err },
		"restore commit": func() error {
			_, err := v.Restore(ctx, "notes/a.md", versions[0].Commit, rev)
			return err
		},
		"commit":       func() error { _, err := v.Commit(ctx, "msg", "N", "n@example.com", nil); return err },
		"import":       func() error { _, err := v.ImportInitial(ctx, "N", "n@example.com"); return err },
		"remove locks": func() error { return v.RemoveGitLocks(ctx) },
		"scan commit":  func() error { return v.Locked(ctx, func() error { return nil }) },
	}
	for name, change := range changes {
		if err := change(); !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s: %v, want ErrReadOnly", name, err)
		}
	}
	if b, _, err := v.Read("notes/a.md"); err != nil || string(b) != "A" {
		t.Fatalf("read: %q, %v", b, err)
	}
	if _, err := v.Status(ctx); err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, p := range []string{"new.md", "folder", "b.md", TrashDir} {
		if _, err := os.Lstat(filepath.Join(dir, p)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after the refused changes: %v", p, err)
		}
	}
}

// A mutation that was waiting for the vault mutex when the vault went read-only is refused
// once it gets it.
func TestAWaitingChangeIsRefusedAfterReadOnly(t *testing.T) {
	v, _ := newVault(t)
	unlock, err := v.lock(ctx)
	must(t, err)
	waiting := make(chan struct{})
	v.onWait = func() { close(waiting) }
	done := make(chan error, 1)
	go func() { _, err := v.Create(ctx, "new.md", []byte("N")); done <- err }()
	<-waiting
	v.SetReadOnly()
	unlock()
	if err := <-done; !errors.Is(err, ErrReadOnly) {
		t.Fatalf("the waiting create: %v, want ErrReadOnly", err)
	}
}
