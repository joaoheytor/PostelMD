//go:build linux

package http

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

func TestSetupImportsTheExistingFiles(t *testing.T) {
	srv, svc := newServer(t, false)
	tok, err := svc.NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	check := func(token string) (int, string) {
		resp, body := do(t, srv, call{method: "POST", path: "/api/v1/setup/check", header: jsonHeader, body: `{"token":"` + token + `"}`})
		return resp.StatusCode, body
	}
	if code, _ := check("guess"); code != 403 {
		t.Fatalf("check with a wrong token = %d, want 403", code)
	}
	if code, body := check(tok); code != 200 || strings.TrimSpace(body) != `{"files":1}` {
		t.Fatalf("check = %d %s, want the one file setup will commit", code, body)
	}
	resp, body := do(t, srv, call{method: "POST", path: "/api/v1/setup", header: jsonHeader,
		body: `{"token":"` + tok + `","password":"` + password + `","name":"Ada","email":"ada@example.invalid"}`})
	if resp.StatusCode != 201 || strings.TrimSpace(body) != `{"imported":1}` {
		t.Fatalf("setup = %d %s", resp.StatusCode, body)
	}
	if code, _ := check(tok); code != 409 {
		t.Fatalf("check after setup = %d, want 409", code)
	}
	cookie := session(resp)
	resp, body = do(t, srv, call{method: "GET", path: "/api/v1/history/a.md", header: withCookie(cookie, nil)})
	if resp.StatusCode != 200 || !strings.Contains(body, `"message":"Initial import"`) || !strings.Contains(body, `"author":"Ada"`) {
		t.Fatalf("history after setup = %d %s", resp.StatusCode, body)
	}
}

func TestGitAPI(t *testing.T) {
	srv, svc := newServer(t, false)
	cookie := setup(t, srv, svc)
	auth := withCookie(cookie, nil)
	authJSON := withCookie(cookie, jsonHeader)
	get := func(path string) (int, string) {
		resp, body := do(t, srv, call{method: "GET", path: path, header: auth})
		return resp.StatusCode, body
	}

	for _, path := range []string{"/api/v1/git/status", "/api/v1/history/a.md"} {
		if resp, _ := do(t, srv, call{method: "GET", path: path}); resp.StatusCode != 401 {
			t.Fatalf("%s without a session = %d", path, resp.StatusCode)
		}
	}
	if code, body := get("/api/v1/git/status"); code != 200 || !strings.Contains(body, `"changes":[]`) || !strings.Contains(body, `"locks":[]`) {
		t.Fatalf("status after setup = %d %s", code, body)
	}
	resp, _ := do(t, srv, call{method: "PUT", path: "/api/v1/notes/a.md", body: "A2",
		header: withCookie(cookie, map[string]string{"If-Match": `"559aead08264d5795d3909718cdd05abd49572e84fe55590eef31a88a08fdffd"`})})
	if resp.StatusCode != 204 {
		t.Fatalf("save = %d", resp.StatusCode)
	}
	editRev := resp.Header.Get("ETag")
	if code, body := get("/api/v1/git/status"); !strings.Contains(body, `{"path":"a.md","kind":"modified"}`) {
		t.Fatalf("status after an edit = %d %s", code, body)
	}
	if resp, _ := do(t, srv, call{method: "POST", path: "/api/v1/git/commit", header: authJSON, body: `{"message":"  "}`}); resp.StatusCode != 422 {
		t.Fatalf("an empty message = %d, want 422", resp.StatusCode)
	}
	resp, body := do(t, srv, call{method: "POST", path: "/api/v1/git/commit", header: authJSON, body: `{"message":"Edit a"}`})
	if resp.StatusCode != 201 {
		t.Fatalf("commit = %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, srv, call{method: "POST", path: "/api/v1/git/commit", header: authJSON, body: `{"message":"again"}`}); resp.StatusCode != 409 {
		t.Fatalf("nothing to commit = %d, want 409", resp.StatusCode)
	}

	_, body = get("/api/v1/history/a.md")
	var hist struct {
		Versions []struct {
			Commit, Message string
		}
	}
	if err := json.Unmarshal([]byte(body), &hist); err != nil || len(hist.Versions) != 2 || hist.Versions[0].Message != "Edit a" {
		t.Fatalf("history = %s (%v)", body, err)
	}
	first := hist.Versions[1].Commit
	resp, body = do(t, srv, call{method: "GET", path: "/api/v1/versions/" + first + "/a.md", header: auth})
	if resp.StatusCode != 200 || body != "A" || resp.Header.Get("ETag") != "" ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("old version = %d %q, ETag %q", resp.StatusCode, body, resp.Header.Get("ETag"))
	}
	for _, bad := range []string{"/api/v1/versions/HEAD/a.md", "/api/v1/versions/" + first + "/missing.md", "/api/v1/history/.git/config"} {
		if code, _ := get(bad); code != 404 {
			t.Errorf("GET %s = %d, want 404", bad, code)
		}
	}

	restore := func(ifMatch string) (int, string) {
		h := withCookie(cookie, jsonHeader)
		if ifMatch != "" {
			h["If-Match"] = ifMatch
		}
		resp, body := do(t, srv, call{method: "POST", path: "/api/v1/restore", header: h, body: `{"path":"a.md","commit":"` + first + `"}`})
		return resp.StatusCode, body
	}
	if code, _ := restore(""); code != 428 {
		t.Fatalf("restore without If-Match = %d, want 428", code)
	}
	if code, body := restore(`"` + strings.Repeat("0", 64) + `"`); code != 412 || body != "A2" {
		t.Fatalf("restore over a newer edit = %d %q, want 412 with the current note", code, body)
	}
	if code, _ := restore(editRev); code != 204 {
		t.Fatalf("restore = %d", code)
	}
	if _, body := get("/api/v1/notes/a.md"); body != "A" {
		t.Fatalf("after restore the note is %q", body)
	}
}

// TestUnsafeExcludeIsExplained: the commit dialog shows why status and commit refuse a
// symlinked or special .git/info/exclude, and how to fix it.
func TestUnsafeExcludeIsExplained(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/git/commit", nil)
	(&GitAPI{}).fail(rec, req, fmt.Errorf("wrapped: %w", vault.ErrUnsafeExclude))
	if body := rec.Body.String(); rec.Code != 409 || !strings.Contains(body, ".git/info/exclude") || !strings.Contains(body, "regular file") {
		t.Fatalf("fail = %d %s", rec.Code, body)
	}
}

func TestGitLockNeedsConfirmation(t *testing.T) {
	srv, svc := newServer(t, false)
	cookie := setup(t, srv, svc)
	authJSON := withCookie(cookie, jsonHeader)
	resp, _ := do(t, srv, call{method: "POST", path: "/api/v1/git/locks/remove", header: authJSON, body: `{"confirmNoOtherGit":false}`})
	if resp.StatusCode != 422 {
		t.Fatalf("removing locks without confirmation = %d, want 422", resp.StatusCode)
	}
	resp, _ = do(t, srv, call{method: "POST", path: "/api/v1/git/locks/remove", header: authJSON, body: `{"confirmNoOtherGit":true}`})
	if resp.StatusCode != 204 {
		t.Fatalf("removing locks = %d", resp.StatusCode)
	}
}
