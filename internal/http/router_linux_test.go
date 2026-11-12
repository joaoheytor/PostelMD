//go:build linux

package http

import (
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/auth"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

const password = "correct horse battery"

func newServer(t *testing.T, tls bool) (*httptest.Server, *auth.Service) {
	t.Helper()
	return newLoggingServer(t, tls, slog.New(slog.DiscardHandler))
}

// newLoggingServer is newServer with the router logging to log.
func newLoggingServer(t *testing.T, tls bool, log *slog.Logger) (*httptest.Server, *auth.Service) {
	t.Helper()
	base := t.TempDir()
	state, notes := filepath.Join(base, "state"), filepath.Join(base, "notes")
	for _, d := range []string{state, notes} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(notes, "a.md"), []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := auth.OpenDir(state, audit.New(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	v, err := vault.Open(notes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	if _, err := v.EnableGit(); err != nil {
		t.Fatal(err)
	}
	h := NewRouter(svc, v, testUI, testBuild, log)
	srv := httptest.NewUnstartedServer(h)
	if tls {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv, svc
}

// session returns the session cookie a response set, or nil.
func session(resp *stdhttp.Response) *stdhttp.Cookie {
	for _, c := range resp.Cookies() {
		if strings.HasSuffix(c.Name, cookieName) {
			return c
		}
	}
	return nil
}

func withCookie(c *stdhttp.Cookie, h map[string]string) map[string]string {
	out := map[string]string{"Cookie": c.Name + "=" + c.Value}
	for k, v := range h {
		out[k] = v
	}
	return out
}

var jsonHeader = map[string]string{"Content-Type": "application/json"}

func setup(t *testing.T, srv *httptest.Server, svc *auth.Service) *stdhttp.Cookie {
	t.Helper()
	tok, err := svc.NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	resp, body := do(t, srv, call{method: "POST", path: "/api/v1/setup", header: jsonHeader,
		body: `{"token":"` + tok + `","password":"` + password + `","name":"Ada","email":"ada@example.invalid"}`})
	if resp.StatusCode != 201 || session(resp) == nil {
		t.Fatalf("setup: %d %s", resp.StatusCode, body)
	}
	return session(resp)
}

func TestSessionsGuardTheNotes(t *testing.T) {
	srv, svc := newServer(t, false)
	cookie := setup(t, srv, svc)

	if resp, _ := do(t, srv, call{method: "GET", path: "/api/v1/notes/a.md"}); resp.StatusCode != 401 {
		t.Fatalf("notes without a session = %d, want 401", resp.StatusCode)
	}
	if resp, _ := do(t, srv, call{method: "GET", path: "/api/v1/files"}); resp.StatusCode != 401 {
		t.Fatalf("the file list without a session = %d, want 401", resp.StatusCode)
	}
	forged := &stdhttp.Cookie{Name: cookieName, Value: "AQ.AQ", HttpOnly: true, Secure: true, SameSite: stdhttp.SameSiteLaxMode}
	if resp, _ := do(t, srv, call{method: "GET", path: "/api/v1/notes/a.md", header: withCookie(forged, nil)}); resp.StatusCode != 401 {
		t.Fatalf("notes with a forged session = %d, want 401", resp.StatusCode)
	}
	resp, body := do(t, srv, call{method: "GET", path: "/api/v1/notes/a.md", header: withCookie(cookie, nil)})
	if resp.StatusCode != 200 || body != "A" {
		t.Fatalf("notes with a session = %d %q", resp.StatusCode, body)
	}
	if c := session(resp); c != nil {
		t.Fatal("a read set a cookie")
	}
	if resp, _ := do(t, srv, call{method: "POST", path: "/api/v1/setup", header: jsonHeader, body: `{}`}); resp.StatusCode != 409 && resp.StatusCode != 422 {
		t.Fatalf("setup after setup = %d", resp.StatusCode)
	}
	h := resp.Header
	if h.Get("X-Frame-Options") != "DENY" || h.Get("Referrer-Policy") != "no-referrer" || !strings.HasPrefix(h.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("security headers: %v", h)
	}
}

func TestCookieAttributes(t *testing.T) {
	srv, svc := newServer(t, false)
	c := setup(t, srv, svc)
	if c.Name != cookieName || !c.HttpOnly || c.SameSite != stdhttp.SameSiteLaxMode || c.Path != "/" || c.Secure {
		t.Fatalf("plain-HTTP cookie: %+v", c)
	}
	tlsSrv, tlsSvc := newServer(t, true)
	tc := setup(t, tlsSrv, tlsSvc)
	if tc.Name != hostCookieKey || !tc.Secure || !tc.HttpOnly {
		t.Fatalf("TLS cookie: %+v", tc)
	}
	if resp, _ := do(t, tlsSrv, call{method: "GET", path: "/api/v1/session", header: withCookie(tc, nil)}); resp.StatusCode != 204 {
		t.Fatalf("the __Host- cookie isn't read back over TLS: %d", resp.StatusCode)
	}
}

func TestCrossOriginWritesAreRefused(t *testing.T) {
	srv, svc := newServer(t, false)
	cookie := setup(t, srv, svc)
	cross := withCookie(cookie, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"})
	for _, c := range []call{
		{method: "POST", path: "/api/v1/folders/x", header: cross},
		{method: "DELETE", path: "/api/v1/notes/a.md", header: cross},
		{method: "POST", path: "/api/v1/session/revoke", header: cross},
	} {
		if resp, _ := do(t, srv, c); resp.StatusCode != 403 {
			t.Errorf("cross-site %s %s = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
	same := withCookie(cookie, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if resp, _ := do(t, srv, call{method: "POST", path: "/api/v1/folders/x", header: same}); resp.StatusCode != 201 {
		t.Fatalf("same-origin mkdir = %d", resp.StatusCode)
	}
}

func login(t *testing.T, srv *httptest.Server, pw string) *stdhttp.Response {
	t.Helper()
	resp, _ := do(t, srv, call{method: "POST", path: "/api/v1/session", header: jsonHeader, body: `{"password":"` + pw + `"}`})
	return resp
}

func TestLoginBackoff(t *testing.T) {
	srv, svc := newServer(t, false)
	cookie := setup(t, srv, svc)
	if resp := login(t, srv, password); resp.StatusCode != 204 || session(resp) == nil {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	for i := range 6 {
		resp := login(t, srv, "not the password")
		if resp.StatusCode != 401 {
			t.Fatalf("wrong password = %d, want 401", resp.StatusCode)
		}
		// The failure that starts the backoff says so, so the form doesn't just say "wrong
		// password" while the next attempt would be refused.
		if want := map[bool]string{true: "1", false: ""}[i == 5]; resp.Header.Get("Retry-After") != want {
			t.Fatalf("failure %d: Retry-After %q, want %q", i+1, resp.Header.Get("Retry-After"), want)
		}
	}
	if resp := login(t, srv, password); resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("throttled login = %d, Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	// Guessing the current password through a password change is throttled the same way.
	resp, _ := do(t, srv, call{method: "PUT", path: "/api/v1/password", header: withCookie(cookie, jsonHeader),
		body: `{"current":"` + password + `","next":"a brand new password"}`})
	if resp.StatusCode != 429 {
		t.Fatalf("password change while throttled = %d, want 429", resp.StatusCode)
	}
}

func TestPasswordChangeAndLogout(t *testing.T) {
	srv, svc := newServer(t, false)
	first := setup(t, srv, svc)
	change := func(body string) *stdhttp.Response {
		resp, _ := do(t, srv, call{method: "PUT", path: "/api/v1/password", header: withCookie(first, jsonHeader), body: body})
		return resp
	}
	check := func(c *stdhttp.Cookie) int {
		resp, _ := do(t, srv, call{method: "GET", path: "/api/v1/session", header: withCookie(c, nil)})
		return resp.StatusCode
	}
	if resp := change(`{"current":"guess","next":"a brand new password"}`); resp.StatusCode != 401 {
		t.Fatalf("change without the current password = %d, want 401", resp.StatusCode)
	}
	if resp := change(`{"current":"` + password + `","next":"short"}`); resp.StatusCode != 422 {
		t.Fatalf("change to a weak password = %d, want 422", resp.StatusCode)
	}
	resp := change(`{"current":"` + password + `","next":"a brand new password"}`)
	fresh := session(resp)
	if resp.StatusCode != 204 || fresh == nil {
		t.Fatalf("password change = %d", resp.StatusCode)
	}
	if check(first) != 401 || check(fresh) != 204 {
		t.Fatalf("after a password change: old %d, new %d", check(first), check(fresh))
	}
	resp, _ = do(t, srv, call{method: "DELETE", path: "/api/v1/session", header: withCookie(fresh, nil)})
	if cl := session(resp); resp.StatusCode != 204 || cl == nil || cl.MaxAge >= 0 {
		t.Fatalf("logout didn't clear the cookie: %d %+v", resp.StatusCode, cl)
	}
	resp, _ = do(t, srv, call{method: "POST", path: "/api/v1/session/revoke", header: withCookie(fresh, nil)})
	if resp.StatusCode != 204 || check(fresh) != 401 {
		t.Fatalf("revoke = %d; session still valid: %v", resp.StatusCode, check(fresh) == 204)
	}
}
