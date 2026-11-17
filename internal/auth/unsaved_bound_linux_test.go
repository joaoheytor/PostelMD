//go:build linux

package auth

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// badSync is a state directory whose fsync of auth.json's temp file fails with EIO while
// failing is set (a failing disk; #189). It counts the steps taken on auth.json and its
// temp files, so a test can tell which requests touched the disk.
type badSync struct {
	failing bool
	steps   int
}

func (h *badSync) Before(op fs.Op) error {
	if !strings.HasPrefix(op.Path, credentialFile) {
		return nil
	}
	h.steps++
	if h.failing && op.Kind == fs.OpSyncFile {
		return syscall.EIO
	}
	return nil
}

func (h *badSync) After(fs.Op, error) {}

// withBadSync is newEnv with its state directory reporting to a badSync hook.
func withBadSync(t *testing.T) (*env, *badSync) {
	t.Helper()
	e := newEnv(t)
	h := &badSync{}
	root, err := fs.OpenRoot(e.dir, fs.WithHook(h))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	e.root = root
	return e, h
}

func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range entries {
		if strings.HasPrefix(de.Name(), tmpPrefix) {
			names = append(names, de.Name())
		}
	}
	return names
}

// TestAFailedSyncLeavesNoTempFile: when the fsync of auth.json's temp file fails, neither
// the change nor any retry of it leaves its temp file behind (#189).
func TestAFailedSyncLeavesNoTempFile(t *testing.T) {
	e, h := withBadSync(t)
	s, cookie := setUp(t, e)
	h.failing = true
	if err := s.LogoutEverywhere(ctx, cookie); !errors.Is(err, ErrNotSaved) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("revocation with a failing fsync = %v, want ErrNotSaved with EIO", err)
	}
	for range 3 {
		e.now = e.now.Add(retryEvery)
		_ = s.Check("any cookie")
	}
	if left := tempFiles(t, e.dir); len(left) != 0 {
		t.Fatalf("failed fsyncs left temp files behind: %v", left)
	}
}

// TestAnUnsavedChangeIsRetriedAtMostEveryFewSeconds: while a change is held only in memory,
// logins and session checks retry the save at most once every retryEvery; the requests in
// between don't touch the disk (#189, ADR-0039). One warning, then "now saved".
func TestAnUnsavedChangeIsRetriedAtMostEveryFewSeconds(t *testing.T) {
	e, h := withBadSync(t)
	s, cookie := setUp(t, e)
	other, _ := s.Login(ctx, "ip", pw)
	h.failing = true
	if err := s.LogoutEverywhere(ctx, cookie); !errors.Is(err, ErrNotSaved) {
		t.Fatalf("revocation = %v, want ErrNotSaved", err)
	}
	requests := func(at time.Duration) int {
		t.Helper()
		before := h.steps
		if s.Check(other) == nil || s.Check(cookie) == nil {
			t.Fatalf("a revoked cookie works at +%s while the change is held or retried", at)
		}
		if _, err := s.Login(ctx, "ip2", pw); err != nil {
			t.Fatalf("login at +%s: %v", at, err)
		}
		_ = s.Check("any cookie")
		return h.steps - before
	}
	start := e.now
	at := func(d time.Duration) int { e.now = start.Add(d); return requests(d) }
	if n := at(0); n != 0 {
		t.Fatalf("requests right after the failed save took %d disk steps, want none", n)
	}
	if n := at(retryEvery - time.Millisecond); n != 0 {
		t.Fatalf("requests before retryEvery took %d disk steps, want none", n)
	}
	if n := at(retryEvery); n == 0 {
		t.Fatal("no request retried the save once retryEvery had passed")
	}
	if n := at(retryEvery + time.Millisecond); n != 0 {
		t.Fatalf("requests right after a failed retry took %d disk steps, want none", n)
	}
	h.failing = false
	if n := at(2*retryEvery - time.Millisecond); n != 0 {
		t.Fatalf("requests before the next retry took %d disk steps, want none", n)
	}
	if n := at(2 * retryEvery); n == 0 {
		t.Fatal("no request retried the save once the disk worked again")
	}
	if n := at(3 * retryEvery); n != 0 {
		t.Fatalf("requests after the save took %d disk steps, want none", n)
	}
	out := e.logs.String()
	if w, saved := strings.Count(out, "held only in memory"), strings.Count(out, "now saved"); w != 1 || saved != 1 {
		t.Fatalf("logged %d warnings and %d saves, want one of each:\n%s", w, saved, out)
	}
	if restarted := e.open(t); restarted.Check(cookie) == nil {
		t.Fatal("the retry didn't save the revocation: a restart brings the session back")
	}
}
