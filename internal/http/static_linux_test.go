//go:build linux

package http

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

var testUI = fstest.MapFS{
	"index.html":         {Data: []byte("<!doctype html><title>PostelMD</title>")},
	"assets/app-1a2b.js": {Data: []byte("export {}")},
}

// appCSP is ADR-0007's policy: scripts, styles and Workers only from our origin, no inline
// script or style, and Trusted Types with exactly these policy names.
const appCSP = "default-src 'self'; script-src 'self'; style-src 'self'; worker-src 'self'; " +
	"img-src 'self' blob: data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; " +
	"require-trusted-types-for 'script'; trusted-types postelmd-preview dompurify lit-html"

func TestWebApp(t *testing.T) {
	srv, svc := newServer(t, false)
	if resp, _ := do(t, srv, call{method: "GET", path: "/setup"}); resp.StatusCode != 200 {
		t.Fatalf("GET /setup before setup = %d, want the app", resp.StatusCode)
	}
	c := setup(t, srv, svc)
	for _, path := range []string{"/", "/notes/runbooks/db.md", "/login"} {
		resp, body := do(t, srv, call{method: "GET", path: path})
		if resp.StatusCode != 200 || !strings.Contains(body, "<title>PostelMD") || resp.Header.Get("Cache-Control") != "no-cache" {
			t.Errorf("GET %s = %d %q, cache %q; want index.html, revalidated", path, resp.StatusCode, body, resp.Header.Get("Cache-Control"))
		}
		if got := resp.Header.Get("Content-Security-Policy"); got != appCSP {
			t.Errorf("GET %s: CSP %q, want %q", path, got, appCSP)
		}
	}
	resp, body := do(t, srv, call{method: "GET", path: "/assets/app-1a2b.js"})
	if resp.StatusCode != 200 || body != "export {}" || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("asset = %d %q, cache %q", resp.StatusCode, body, resp.Header.Get("Cache-Control"))
	}
	for _, path := range []string{"/assets/missing.js", "/assets/"} {
		resp, _ := do(t, srv, call{method: "GET", path: path})
		if resp.StatusCode != 404 || resp.Header.Get("Cache-Control") != "" {
			t.Errorf("GET %s = %d, cache %q; want an uncached 404", path, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
	}
	if resp, _ := do(t, srv, call{method: "GET", path: "/api/v1/nothing", header: withCookie(c, nil)}); resp.StatusCode != 404 ||
		resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("an unknown API path = %d %s; want a JSON 404, never the app", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := do(t, srv, call{method: "POST", path: "/", header: jsonHeader}); resp.StatusCode != 405 {
		t.Fatalf("POST / = %d, want 405", resp.StatusCode)
	}
}

func TestWithoutTheWebApp(t *testing.T) {
	w := httptest.NewRecorder()
	webApp(nil).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 404 || !strings.Contains(w.Body.String(), "make build") {
		t.Fatalf("no UI = %d %q", w.Code, w.Body.String())
	}
}
