//go:build linux

package http

import (
	stdhttp "net/http"
	"strings"
	"testing"
)

// TestPreSetupRouting checks ADR-0009's pre-setup table, one subtest per row: until an
// admin exists, setup, health and the setup page's assets are served, an HTML navigation
// anywhere else is sent to /setup, any other API path is 503, and /metrics is 404.
func TestPreSetupRouting(t *testing.T) {
	srv, _ := newServer(t, false)
	srv.Client().CheckRedirect = func(*stdhttp.Request, []*stdhttp.Request) error { return stdhttp.ErrUseLastResponse }
	html := map[string]string{"Accept": "text/html,application/xhtml+xml"}
	tests := []struct {
		row      string // the ADR-0009 row
		call     call
		want     int
		wantBody string // substring
	}{
		{"setup page served", call{method: "GET", path: "/setup", header: html}, 200, "<title>PostelMD"},
		{"setup API served", call{method: "POST", path: "/api/v1/setup", header: jsonHeader,
			body: `{"token":"guess","password":"` + password + `","name":"Ada","email":"a@b"}`}, 403, ""},
		{"healthz served", call{method: "GET", path: "/healthz"}, 200, ""},
		{"readyz served", call{method: "GET", path: "/readyz"}, 200, ""},
		{"assets served", call{method: "GET", path: "/assets/app-1a2b.js"}, 200, "export {}"},
		{"a missing asset is a 404, not a redirect", call{method: "GET", path: "/assets/missing.js"}, 404, ""},
		{"HTML navigation to the root redirects", call{method: "GET", path: "/", header: html}, 303, ""},
		{"HTML navigation to a note page redirects", call{method: "GET", path: "/notes/runbooks/db.md", header: html}, 303, ""},
		{"HTML navigation to the login page redirects", call{method: "GET", path: "/login", header: html}, 303, ""},
		{"a navigation without Accept redirects", call{method: "GET", path: "/some/page"}, 303, ""},
		{"a note read is 503", call{method: "GET", path: "/api/v1/notes/a.md"}, 503, "setup required"},
		{"the file list is 503", call{method: "GET", path: "/api/v1/files"}, 503, "setup required"},
		{"the session is 503", call{method: "GET", path: "/api/v1/session"}, 503, "setup required"},
		{"an unknown API path is 503", call{method: "GET", path: "/api/v1/nothing"}, 503, "setup required"},
		{"an API path asked for as HTML is 503", call{method: "GET", path: "/api/v1/files", header: html}, 503, "setup required"},
		{"metrics is 404", call{method: "GET", path: "/metrics"}, 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.row, func(t *testing.T) {
			resp, body := do(t, srv, tt.call)
			if resp.StatusCode != tt.want || !strings.Contains(body, tt.wantBody) {
				t.Fatalf("%s %s before setup = %d %q, want %d containing %q",
					tt.call.method, tt.call.path, resp.StatusCode, body, tt.want, tt.wantBody)
			}
			if loc := resp.Header.Get("Location"); (tt.want == 303) != (loc == "/setup") {
				t.Fatalf("%s %s: Location %q", tt.call.method, tt.call.path, loc)
			}
		})
	}
}
