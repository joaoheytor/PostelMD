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
		if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("GET %s without the CSP", path)
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
