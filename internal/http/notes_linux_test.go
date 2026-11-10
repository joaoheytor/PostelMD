//go:build linux

package http

import (
	"encoding/json"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

func newAPI(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes", "a.md"), []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	srv := httptest.NewServer(NewNotesAPI(v, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)
	return srv, dir
}

type call struct {
	method, path, body string
	header             map[string]string
}

func do(t *testing.T, srv *httptest.Server, c call) (*stdhttp.Response, string) {
	t.Helper()
	req, err := stdhttp.NewRequest(c.method, srv.URL+c.path, strings.NewReader(c.body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b)
}

func revA() string { return etag(vault.RevOf([]byte("A"))) }

func TestNotesAPI(t *testing.T) {
	srv, dir := newAPI(t)
	tests := []struct {
		name       string
		call       call
		wantStatus int
		wantBody   string // substring
		wantETag   string
	}{
		{"read", call{method: "GET", path: "/api/v1/notes/notes/a.md"}, 200, "A", revA()},
		{"read missing", call{method: "GET", path: "/api/v1/notes/notes/x.md"}, 404, "not found", ""},
		{"read reserved", call{method: "GET", path: "/api/v1/notes/.git/config"}, 404, "", ""},
		{"read escape", call{method: "GET", path: "/api/v1/notes/..%2f..%2fetc%2fpasswd"}, 404, "", ""},
		{"no precondition", call{method: "PUT", path: "/api/v1/notes/notes/a.md", body: "B"}, 428, "", ""},
		{"both preconditions", call{method: "PUT", path: "/api/v1/notes/notes/a.md", body: "B",
			header: map[string]string{"If-Match": revA(), "If-None-Match": "*"}}, 428, "", ""},
		{"weak etag", call{method: "PUT", path: "/api/v1/notes/notes/a.md", body: "B",
			header: map[string]string{"If-Match": "W/" + revA()}}, 400, "", ""},
		{"stale save returns the current note", call{method: "PUT", path: "/api/v1/notes/notes/a.md", body: "B",
			header: map[string]string{"If-Match": etag(vault.RevOf([]byte("old")))}}, 412, "A", revA()},
		{"autosave never creates", call{method: "PUT", path: "/api/v1/notes/notes/gone.md", body: "B",
			header: map[string]string{"If-Match": revA()}}, 404, "", ""},
		{"save", call{method: "PUT", path: "/api/v1/notes/notes/a.md", body: "B",
			header: map[string]string{"If-Match": revA()}}, 204, "", etag(vault.RevOf([]byte("B")))},
		{"create", call{method: "PUT", path: "/api/v1/notes/notes/new.md", body: "N",
			header: map[string]string{"If-None-Match": "*"}}, 201, "", etag(vault.RevOf([]byte("N")))},
		{"create over a note", call{method: "PUT", path: "/api/v1/notes/notes/a.md", body: "X",
			header: map[string]string{"If-None-Match": "*"}}, 409, "", ""},
		{"mkdir", call{method: "POST", path: "/api/v1/folders/archive"}, 201, "", ""},
		{"move needs JSON", call{method: "POST", path: "/api/v1/move", body: `{"from":"notes/a.md","to":"archive/a.md"}`}, 415, "", ""},
		{"move", call{method: "POST", path: "/api/v1/move", body: `{"from":"notes/a.md","to":"archive/a.md"}`,
			header: map[string]string{"Content-Type": "application/json"}}, 204, "", ""},
		{"move onto a note", call{method: "POST", path: "/api/v1/move", body: `{"from":"notes/new.md","to":"archive/a.md"}`,
			header: map[string]string{"Content-Type": "application/json"}}, 409, "", ""},
		{"move a folder into itself", call{method: "POST", path: "/api/v1/move", body: `{"from":"notes","to":"notes/sub/notes"}`,
			header: map[string]string{"Content-Type": "application/json"}}, 400, `{"error":"a folder can't move into itself"}`, ""},
		{"list root", call{method: "GET", path: "/api/v1/folders/"}, 200, `{"entries":[{"name":"archive","kind":"folder"},{"name":"notes","kind":"folder"}]}`, ""},
		{"delete", call{method: "DELETE", path: "/api/v1/notes/archive/a.md"}, 200, "trashName", ""},
		{"delete a folder", call{method: "DELETE", path: "/api/v1/folders/notes"}, 200, "trashName", ""},
	}
	for _, tt := range tests {
		resp, body := do(t, srv, tt.call)
		if resp.StatusCode != tt.wantStatus || !strings.Contains(body, tt.wantBody) {
			t.Errorf("%s: %d %q, want %d containing %q", tt.name, resp.StatusCode, body, tt.wantStatus, tt.wantBody)
		}
		if tt.wantETag != "" && resp.Header.Get("ETag") != tt.wantETag {
			t.Errorf("%s: ETag %q, want %q", tt.name, resp.Header.Get("ETag"), tt.wantETag)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "notes", "gone.md")); err == nil {
		t.Fatal("an autosave created a note")
	}
}

func TestNotesAreServedAsInertData(t *testing.T) {
	srv, dir := newAPI(t)
	if err := os.WriteFile(filepath.Join(dir, "page.html"), []byte("<script>alert(1)</script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	for p, wantType := range map[string]string{"notes/a.md": "text/markdown; charset=utf-8", "page.html": "application/octet-stream"} {
		resp, _ := do(t, srv, call{method: "GET", path: "/api/v1/notes/" + p})
		h := resp.Header
		if h.Get("Content-Type") != wantType || h.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.HasPrefix(h.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s: headers %v", p, h)
		}
	}
}

func TestNotesAPITooLarge(t *testing.T) {
	srv, _ := newAPI(t)
	resp, _ := do(t, srv, call{method: "PUT", path: "/api/v1/notes/notes/a.md",
		body: strings.Repeat("x", vault.MaxNoteSize+1), header: map[string]string{"If-Match": revA()}})
	if resp.StatusCode != 413 {
		t.Fatalf("status %d, want 413", resp.StatusCode)
	}
}

func TestListShape(t *testing.T) {
	srv, _ := newAPI(t)
	_, body := do(t, srv, call{method: "GET", path: "/api/v1/folders/notes"})
	var got struct{ Entries []vault.Entry }
	if err := json.Unmarshal([]byte(body), &got); err != nil || len(got.Entries) != 1 || got.Entries[0].Name != "a.md" {
		t.Fatalf("list notes = %s (%v)", body, err)
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("open a\nfake=entry\r"); got != `open a\nfake=entry\r` {
		t.Fatalf("oneLine = %q", got)
	}
}
