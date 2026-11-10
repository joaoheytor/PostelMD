//go:build linux

package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

var ctx = context.Background()

const pw = "correct horse battery"

type env struct {
	dir  string
	root *fs.Root
	logs *bytes.Buffer
	now  time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	root, err := fs.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return &env{dir: dir, root: root, logs: &bytes.Buffer{}, now: time.Unix(1_800_000_000, 0)}
}

func (e *env) open(t *testing.T) *Service {
	t.Helper()
	s, err := Open(e.root, audit.New(slog.New(slog.NewTextHandler(e.logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return e.now }
	return s
}

func setUp(t *testing.T, e *env) (*Service, string) {
	t.Helper()
	s := e.open(t)
	tok, err := s.NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := s.Setup(ctx, tok, pw, "Ada", "ada@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	return s, cookie
}

func TestSetup(t *testing.T) {
	e := newEnv(t)
	s := e.open(t)
	if !s.NeedsSetup() {
		t.Fatal("a new instance doesn't need setup")
	}
	if _, err := s.Setup(ctx, "", pw, "Ada", "ada@example.invalid"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("setup without a token = %v", err)
	}
	tok, _ := s.NewSetupToken()
	for _, bad := range [][2]string{{"", "a@b"}, {"Ada", "no-at"}, {"Ada\n", "a@b"}, {"Ada", "<a@b>"}} {
		if _, err := s.Setup(ctx, tok, pw, bad[0], bad[1]); !errors.Is(err, ErrBadProfile) {
			t.Errorf("setup with %q = %v, want ErrBadProfile", bad, err)
		}
	}
	if _, err := s.Setup(ctx, tok, "short", "Ada", "a@b"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password = %v", err)
	}
	if _, err := s.Setup(ctx, tok+"x", pw, "Ada", "a@b"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("wrong token = %v", err)
	}
	e.now = e.now.Add(SetupTokenTTL)
	if _, err := s.Setup(ctx, tok, pw, "Ada", "a@b"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("expired token = %v", err)
	}
	tok, _ = s.NewSetupToken()
	cookie, err := s.Setup(ctx, tok, pw, "Ada", "ada@example.invalid")
	if err != nil || s.Check(cookie) != nil {
		t.Fatalf("setup = %v; cookie valid: %v", err, s.Check(cookie))
	}
	if _, err := s.Setup(ctx, tok, pw, "Eve", "e@x"); !errors.Is(err, ErrAlreadySetUp) {
		t.Fatalf("second setup = %v", err)
	}
	if _, err := s.NewSetupToken(); !errors.Is(err, ErrAlreadySetUp) {
		t.Fatalf("token after setup = %v", err)
	}
	st, err := os.Stat(filepath.Join(e.dir, credentialFile))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%s: %v, %v", credentialFile, st, err)
	}

	again := e.open(t) // a restart keeps the credential, the secret and the session
	if again.NeedsSetup() || again.Check(cookie) != nil {
		t.Fatal("a restart lost the credential or the session")
	}
	if n, m := again.Author(); n != "Ada" || m != "ada@example.invalid" {
		t.Fatalf("Author = %q, %q", n, m)
	}
}

func TestLoginAndBackoff(t *testing.T) {
	e := newEnv(t)
	s, _ := setUp(t, e)
	if _, err := s.Login(ctx, "198.51.100.1", pw); err != nil {
		t.Fatal(err)
	}
	for i := range freeFailures {
		if _, err := s.Login(ctx, "198.51.100.1", "wrong password!"); !errors.Is(err, ErrBadPassword) {
			t.Fatalf("failure %d = %v", i, err)
		}
	}
	if _, err := s.Login(ctx, "198.51.100.1", "wrong again!!"); !errors.Is(err, ErrBadPassword) {
		t.Fatal("the sixth failure should still be checked")
	}
	var w *WaitError
	if _, err := s.Login(ctx, "198.51.100.1", pw); !errors.As(err, &w) || w.Wait != time.Second {
		t.Fatalf("right after six failures = %v, want a 1 s wait", err)
	}
	if _, err := s.Login(ctx, "203.0.113.7", pw); err != nil {
		t.Fatalf("another IP is never slowed down (no lockout): %v", err)
	}
	e.now = e.now.Add(time.Second)
	if _, err := s.Login(ctx, "198.51.100.1", pw); err != nil {
		t.Fatalf("after the wait: %v", err)
	}
	if _, err := s.Login(ctx, "198.51.100.1", "wrong password!"); !errors.Is(err, ErrBadPassword) {
		t.Fatal("a success didn't reset the backoff")
	}
	if strings.Contains(e.logs.String(), pw) || strings.Contains(e.logs.String(), "wrong password") {
		t.Fatal("a password reached the log")
	}
	for _, ev := range []string{"setup.completed", "login.succeeded", "login.failed", "login.throttled"} {
		if !strings.Contains(e.logs.String(), "event="+ev) {
			t.Errorf("no %s audit record", ev)
		}
	}
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	now := time.Unix(0, 0)
	b := newBackoff(func() time.Time { return now })
	var waits []time.Duration
	for range freeFailures + 25 {
		b.fail("ip")
		waits = append(waits, b.wait("ip"))
	}
	if waits[freeFailures-1] != 0 || waits[freeFailures] != time.Second || waits[freeFailures+1] != 2*time.Second ||
		waits[len(waits)-1] != maxWait {
		t.Fatalf("waits = %v", waits)
	}
}

func TestPasswordChangeAndRevocation(t *testing.T) {
	e := newEnv(t)
	s, cookie := setUp(t, e)
	other, _ := s.Login(ctx, "ip", pw)
	if _, err := s.ChangePassword(ctx, "ip", cookie, "not the password", "a new long password"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("a stolen cookie without the password = %v, want ErrBadPassword", err)
	}
	if _, err := s.ChangePassword(ctx, "ip", "forged", pw, "a new long password"); !errors.Is(err, ErrBadSession) {
		t.Fatalf("change without a session = %v", err)
	}
	fresh, err := s.ChangePassword(ctx, "ip", cookie, pw, "a new long password")
	if err != nil {
		t.Fatal(err)
	}
	if s.Check(cookie) == nil || s.Check(other) == nil || s.Check(fresh) != nil {
		t.Fatal("a password change must revoke every other session and keep the new one")
	}
	if _, err := s.Login(ctx, "ip", pw); !errors.Is(err, ErrBadPassword) {
		t.Fatal("the old password still works")
	}
	if err := s.LogoutEverywhere(ctx, fresh); err != nil || s.Check(fresh) == nil {
		t.Fatalf("log out everywhere = %v; session still valid: %v", err, s.Check(fresh) == nil)
	}
}

func TestResetPassword(t *testing.T) {
	e := newEnv(t)
	if err := ResetPassword(e.dir, pw); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("reset before setup = %v", err)
	}
	_, cookie := setUp(t, e)
	if err := ResetPassword(e.dir, "the reset password"); err != nil {
		t.Fatal(err)
	}
	restarted := e.open(t)
	if restarted.Check(cookie) == nil {
		t.Fatal("a reset must revoke sessions")
	}
	if _, err := restarted.Login(ctx, "ip", "the reset password"); err != nil {
		t.Fatal(err)
	}
	if n, _ := restarted.Author(); n != "Ada" {
		t.Fatal("a reset lost the git author")
	}
}

func TestStateFiles(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(filepath.Join(e.dir, tmpPrefix+"left"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.open(t)
	if _, err := os.Stat(filepath.Join(e.dir, tmpPrefix+"left")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a leftover temp file wasn't removed")
	}
	key, err := os.ReadFile(filepath.Join(e.dir, secretFile))
	if err != nil || len(key) != secretLen {
		t.Fatalf("secret: %d bytes, %v", len(key), err)
	}
	e.open(t)
	if again, _ := os.ReadFile(filepath.Join(e.dir, secretFile)); !bytes.Equal(again, key) {
		t.Fatal("the secret was regenerated")
	}
	if err := os.WriteFile(filepath.Join(e.dir, credentialFile), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(e.root, audit.New(slog.New(slog.DiscardHandler))); err == nil || !strings.Contains(err.Error(), "reset-password") {
		t.Fatalf("a damaged auth.json = %v, want a pointer to reset-password", err)
	}
	if err := os.WriteFile(filepath.Join(e.dir, secretFile), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(e.root, audit.New(slog.New(slog.DiscardHandler))); err == nil {
		t.Fatal("a short secret was accepted")
	}
}
