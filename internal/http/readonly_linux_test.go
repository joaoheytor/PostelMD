//go:build linux

package http

import (
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

// A read-only vault (ADR-0037): /readyz stays 200 and its body says "read-only", a change
// gets 503 without Retry-After (it lasts until a restart), and a read still works.
func TestAReadOnlyVault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	srv := httptest.NewServer(NewNotesAPI(v, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)

	ready := func() (int, string) {
		rec := httptest.NewRecorder()
		readyz(v).ServeHTTP(rec, httptest.NewRequest(stdhttp.MethodGet, "/readyz", nil))
		return rec.Code, rec.Body.String()
	}
	if code, body := ready(); code != 200 || body != "" {
		t.Fatalf("/readyz = %d %q, want 200 and an empty body", code, body)
	}
	v.SetReadOnly()
	if code, body := ready(); code != 200 || body != "read-only\n" {
		t.Fatalf("/readyz read-only = %d %q, want 200 \"read-only\"", code, body)
	}
	for _, c := range []call{
		{method: "PUT", path: "/api/v1/notes/a.md", body: "B", header: map[string]string{"If-Match": revA()}},
		{method: "PUT", path: "/api/v1/notes/new.md", body: "N", header: map[string]string{"If-None-Match": "*"}},
		{method: "DELETE", path: "/api/v1/notes/a.md"},
	} {
		resp, body := do(t, srv, c)
		if resp.StatusCode != 503 || resp.Header.Get("Retry-After") != "" || body != fmt.Sprintf("{%q:%q}\n", "error", readOnlyMessage) {
			t.Errorf("%s %s = %d (Retry-After %q) %s; want 503, no Retry-After, the read-only message", c.method, c.path,
				resp.StatusCode, resp.Header.Get("Retry-After"), body)
		}
	}
	if resp, body := do(t, srv, call{method: "GET", path: "/api/v1/notes/a.md"}); resp.StatusCode != 200 || body != "A" {
		t.Fatalf("a read = %d %q, want 200 \"A\"", resp.StatusCode, body)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "a.md")); err != nil || string(b) != "A" { //nolint:gosec // G304: the test's vault
		t.Fatalf("a.md = %q, %v", b, err)
	}
}

// The trash API answers ErrReadOnly as the notes API does.
func TestTheTrashAPIAnswersReadOnly(t *testing.T) {
	rec := httptest.NewRecorder()
	api := &TrashAPI{notes: &NotesAPI{log: slog.New(slog.DiscardHandler)}, log: slog.New(slog.DiscardHandler)}
	api.fail(rec, httptest.NewRequest(stdhttp.MethodPost, "/api/v1/trash/x/restore", nil), fmt.Errorf("restore: %w", vault.ErrReadOnly))
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("= %d (Retry-After %q), want 503 without Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
}
