//go:build linux

package auth

import (
	"errors"
	"strings"
	"testing"
)

// TestARestartNeverRevivesARevokedCookie: two revocations that can't be saved, a cookie
// issued between them, a restart that reads the older auth.json, and the revocation
// repeated: the repeat must not save the epoch that cookie carries (#188 review).
func TestARestartNeverRevivesARevokedCookie(t *testing.T) {
	e := newEnv(t)
	s, cookie := setUp(t, e)
	writable := readOnly(t, e)
	if err := s.LogoutEverywhere(ctx, cookie); !errors.Is(err, ErrNotSaved) {
		t.Fatalf("first revocation = %v, want ErrNotSaved", err)
	}
	stolen, err := s.Login(ctx, "ip", pw)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LogoutEverywhere(ctx, stolen); !errors.Is(err, ErrNotSaved) {
		t.Fatalf("second revocation = %v, want ErrNotSaved", err)
	}
	writable()
	restarted := e.open(t) // back at the epoch saved before both
	fresh, err := restarted.Login(ctx, "ip2", pw)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.LogoutEverywhere(ctx, fresh); err != nil {
		t.Fatalf("the repeated revocation = %v", err)
	}
	if restarted.Check(stolen) == nil {
		t.Fatal("the repeated revocation saved an epoch a revoked cookie carries: the cookie works again")
	}
}

// TestAnUnsavedChangeIsSavedLater: while a change is held only in memory, the next login
// or session check once retryEvery has passed tries to save it again, and one pathless warning says it is unsaved.
func TestAnUnsavedChangeIsSavedLater(t *testing.T) {
	for _, tc := range []struct {
		name  string
		retry func(s *Service)
	}{
		{"session check", func(s *Service) { _ = s.Check("any cookie") }},
		{"login", func(s *Service) { _, _ = s.Login(ctx, "ip9", pw) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			s, cookie := setUp(t, e)
			other, _ := s.Login(ctx, "ip", pw)
			writable := readOnly(t, e)
			if err := s.LogoutEverywhere(ctx, cookie); !errors.Is(err, ErrNotSaved) {
				t.Fatalf("revocation = %v, want ErrNotSaved", err)
			}
			e.now = e.now.Add(retryEvery)
			tc.retry(s) // still read-only: the retry fails, and isn't logged again
			again, _ := s.Login(ctx, "ip2", pw)
			if err := s.LogoutEverywhere(ctx, again); !errors.Is(err, ErrNotSaved) {
				t.Fatalf("second revocation = %v, want ErrNotSaved", err)
			}
			out := e.logs.String()
			if n := strings.Count(out, "held only in memory"); n != 1 || strings.Contains(out, e.dir) ||
				!strings.Contains(out, "level=WARN") || !strings.Contains(out, "errno=EACCES") {
				t.Fatalf("want one pathless warning naming EACCES, logged %d:\n%s", n, out)
			}
			writable()
			e.now = e.now.Add(retryEvery)
			tc.retry(s)
			if !strings.Contains(e.logs.String(), "now saved") {
				t.Fatalf("the saved retry wasn't logged:\n%s", e.logs.String())
			}
			restarted := e.open(t)
			if restarted.Check(other) == nil || restarted.Check(again) == nil {
				t.Fatal("the retry didn't save the revocation: a restart brings the sessions back")
			}
		})
	}
}
