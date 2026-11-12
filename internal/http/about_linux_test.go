//go:build linux

package http

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// testBuild is the build every test router reports; distinctive, so a leak is easy to find.
var testBuild = Build{Version: "9.8.7-test.6", Commit: "abc1234"}

func TestAboutNeedsASession(t *testing.T) {
	srv, svc := newServer(t, false)
	if resp, _ := do(t, srv, call{method: "GET", path: "/api/v1/about"}); resp.StatusCode != 503 {
		t.Fatalf("about before setup = %d, want 503", resp.StatusCode)
	}
	cookie := setup(t, srv, svc)

	if resp, body := do(t, srv, call{method: "GET", path: "/api/v1/about"}); resp.StatusCode != 401 || strings.Contains(body, testBuild.Version) {
		t.Fatalf("about without a session = %d %q, want 401 without the version", resp.StatusCode, body)
	}
	resp, body := do(t, srv, call{method: "GET", path: "/api/v1/about", header: withCookie(cookie, nil)})
	var got Build
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &got) != nil || got != testBuild {
		t.Fatalf("about with a session = %d %q, want %+v", resp.StatusCode, body, testBuild)
	}
}

// Nothing a visitor gets before logging in names the version: pages, health checks, headers.
func TestNoVersionBeforeLogin(t *testing.T) {
	srv, svc := newServer(t, false)
	check := func(stage string) {
		t.Helper()
		for _, p := range []string{"/", "/setup", "/login", "/healthz", "/readyz", "/api/v1/session", "/api/v1/about", "/api/v1/nope"} {
			resp, body := do(t, srv, call{method: "GET", path: p})
			all := body + fmt.Sprint(resp.Header)
			if strings.Contains(all, testBuild.Version) || strings.Contains(all, testBuild.Commit) {
				t.Errorf("%s: GET %s names the build: %d", stage, p, resp.StatusCode)
			}
		}
	}
	check("before setup")
	setup(t, srv, svc)
	check("after setup, logged out")
}
