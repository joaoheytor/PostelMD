//go:build linux

package auth

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// readOnly makes the state directory unwritable, so saving auth.json fails as it does on a
// full disk, until the returned func restores it.
func readOnly(t *testing.T, e *env) func() {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes read-only directories")
	}
	restore := func() { _ = os.Chmod(e.dir, 0o700) } //nolint:gosec // removing a directory needs its x bit
	if err := os.Chmod(e.dir, 0o500); err != nil {   //nolint:gosec // read-only, but still listable
		t.Fatal(err)
	}
	t.Cleanup(restore)
	return restore
}

// TestUnsavedRevocationFailsClosed: "log out everywhere" whose auth.json can't be saved
// still revokes every session in memory, says the change wasn't saved, and is audited
// (#187). Repeating it once the disk is writable saves it, so a restart keeps it.
func TestUnsavedRevocationFailsClosed(t *testing.T) {
	e := newEnv(t)
	s, cookie := setUp(t, e)
	other, _ := s.Login(ctx, "ip", pw)
	writable := readOnly(t, e)
	err := s.LogoutEverywhere(ctx, cookie)
	if s.Check(cookie) == nil || s.Check(other) == nil {
		t.Fatalf("a stolen cookie still works after a revocation that couldn't be saved (%v)", err)
	}
	if !errors.Is(err, ErrNotSaved) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("log out everywhere without a writable auth.json = %v, want ErrNotSaved with its cause", err)
	}
	if out := e.logs.String(); !strings.Contains(out, "event=sessions.revoked saved=false") {
		t.Fatalf("the unsaved revocation wasn't audited as unsaved: %s", out)
	}
	writable()
	fresh, err := s.Login(ctx, "ip", pw)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LogoutEverywhere(ctx, fresh); err != nil {
		t.Fatalf("repeating it with a writable auth.json = %v", err)
	}
	if restarted := e.open(t); restarted.Check(other) == nil || restarted.Check(fresh) == nil {
		t.Fatal("the repeated revocation wasn't saved")
	}
}

// TestUnsavedPasswordChangeFailsClosed: a password change whose auth.json can't be saved
// still revokes every session and the old password in memory, says it wasn't saved, and is
// audited (#187). Repeating it once the disk is writable saves it.
func TestUnsavedPasswordChangeFailsClosed(t *testing.T) {
	e := newEnv(t)
	s, cookie := setUp(t, e)
	other, _ := s.Login(ctx, "ip", pw)
	writable := readOnly(t, e)
	const next = "a new long password"
	fresh, err := s.ChangePassword(ctx, "ip", cookie, pw, next)
	if s.Check(cookie) == nil || s.Check(other) == nil {
		t.Fatalf("a stolen cookie still works after a password change that couldn't be saved (%v)", err)
	}
	if _, err := s.Login(ctx, "ip2", pw); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("the old password after an unsaved change = %v, want ErrBadPassword", err)
	}
	if !errors.Is(err, ErrNotSaved) || fresh != "" {
		t.Fatalf("a change without a writable auth.json = %q, %v; want no session and ErrNotSaved", fresh, err)
	}
	if out := e.logs.String(); !strings.Contains(out, "event=password.changed ip=ip saved=false") {
		t.Fatalf("the unsaved change wasn't audited as unsaved: %s", out)
	}
	writable()
	again, err := s.Login(ctx, "ip3", next)
	if err != nil {
		t.Fatalf("the new password after an unsaved change: %v", err)
	}
	if _, err := s.ChangePassword(ctx, "ip3", again, next, next); err != nil {
		t.Fatalf("repeating it with a writable auth.json = %v", err)
	}
	restarted := e.open(t)
	if restarted.Check(other) == nil {
		t.Fatal("the repeated change didn't save the revocation")
	}
	if _, err := restarted.Login(ctx, "ip4", next); err != nil {
		t.Fatalf("the repeated change didn't save the password: %v", err)
	}
}
