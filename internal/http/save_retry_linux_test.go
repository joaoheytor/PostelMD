//go:build linux

package http

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

// TestRetriedSaveThatLanded: a PUT whose response was lost is retried with the same, now
// stale If-Match. The note already holds those bytes, so the retry succeeds with the current
// ETag (ADR-0003 §3). The same stale If-Match with other content still gets 412.
func TestRetriedSaveThatLanded(t *testing.T) {
	srv, dir := newAPI(t)
	first := call{method: "PUT", path: "/api/v1/notes/notes/a.md", body: "B", header: map[string]string{"If-Match": revA()}}
	if resp, body := do(t, srv, first); resp.StatusCode != 204 {
		t.Fatalf("first save = %d %q", resp.StatusCode, body)
	}
	resp, body := do(t, srv, first) // the retry
	if want := etag(vault.RevOf([]byte("B"))); resp.StatusCode != 204 || resp.Header.Get("ETag") != want {
		t.Fatalf("retry = %d %q, ETag %q; want 204 with %q", resp.StatusCode, body, resp.Header.Get("ETag"), want)
	}
	other := call{method: "PUT", path: "/api/v1/notes/notes/a.md", body: "C", header: map[string]string{"If-Match": revA()}}
	if resp, body := do(t, srv, other); resp.StatusCode != 412 || body != "B" {
		t.Fatalf("other content with the stale If-Match = %d %q; want 412 with the current note", resp.StatusCode, body)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "notes", "a.md")); err != nil || string(b) != "B" {
		t.Fatalf("note = %q, %v; want B", b, err)
	}
}
