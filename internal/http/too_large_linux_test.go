//go:build linux

package http

import (
	"encoding/json"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

const overReadLimit = 64<<20 + 1 // the store's read limit, plus one byte

// TestWritesOverAFileTooLargeToRead: something outside PostelMD grew a note past the store's
// read limit while it was open. An autosave over it is a final 412 with no current note and
// no ETag, and the code "too-large-to-read" (ADR-0029), never a retried 500 or a 413 that
// would blame the user's note (#157); it isn't logged as a server error, and the file stays
// as it is. Create, move and delete don't read the file, so they answer as for any file.
func TestWritesOverAFileTooLargeToRead(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "a.md")
	if err := os.WriteFile(big, []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	logs := &lockedBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	srv := httptest.NewServer(logWrites(log, NewNotesAPI(v, log)))
	t.Cleanup(srv.Close)
	if err := os.Truncate(big, overReadLimit); err != nil { // sparse: no 64 MiB written
		t.Fatal(err)
	}

	resp, body := do(t, srv, call{method: "PUT", path: "/api/v1/notes/a.md", body: "B", header: map[string]string{"If-Match": revA()}})
	assertTooLargeConflict(t, resp, body, "save")
	if fi, err := os.Stat(big); err != nil || fi.Size() != overReadLimit {
		t.Fatalf("the large file after the save: %v, %v", fi, err)
	}
	if strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Fatalf("a save over a too-large file was logged as an error:\n%s", logs.String())
	}

	for _, c := range []struct {
		call call
		want int
	}{
		{call{method: "PUT", path: "/api/v1/notes/a.md", body: "B", header: map[string]string{"If-None-Match": "*"}}, 409},
		{call{method: "POST", path: "/api/v1/move", body: `{"from":"a.md","to":"b.md"}`, header: jsonHeader}, 204},
		{call{method: "DELETE", path: "/api/v1/notes/b.md"}, 200},
	} {
		if resp, body := do(t, srv, c.call); resp.StatusCode != c.want {
			t.Errorf("%s %s over a file too large to read = %d %s, want %d", c.call.method, c.call.path, resp.StatusCode, body, c.want)
		}
	}
}

// TestRestoreOverAFileTooLargeToRead: a restore from history over a file grown past the read
// limit answers like a save over it (ADR-0029), not with an empty note as the current one.
func TestRestoreOverAFileTooLargeToRead(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "a.md")
	if err := os.WriteFile(big, []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	if _, err := v.EnableGit(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ImportInitial(t.Context(), "Ada", "ada@example.invalid"); err != nil {
		t.Fatal(err)
	}
	hist, err := v.History("a.md", 1)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history = %v, %v", hist, err)
	}
	log := slog.New(slog.DiscardHandler)
	mux := stdhttp.NewServeMux()
	(&GitAPI{git: v, notes: &NotesAPI{notes: v, log: log}, log: log}).register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if err := os.Truncate(big, overReadLimit); err != nil {
		t.Fatal(err)
	}

	h := map[string]string{"Content-Type": "application/json", "If-Match": revA()}
	resp, body := do(t, srv, call{method: "POST", path: "/api/v1/restore", header: h, body: `{"path":"a.md","commit":"` + hist[0].Commit + `"}`})
	assertTooLargeConflict(t, resp, body, "restore")
}

// assertTooLargeConflict: a 412 with the code "too-large-to-read", JSON, and no ETag.
func assertTooLargeConflict(t *testing.T, resp *stdhttp.Response, body, what string) {
	t.Helper()
	var got struct{ Code, Error string }
	if err := json.Unmarshal([]byte(body), &got); err != nil || resp.StatusCode != 412 || got.Code != "too-large-to-read" ||
		resp.Header.Get("ETag") != "" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%s over a file too large to read = %d %v %s, want 412 too-large-to-read without an ETag", what, resp.StatusCode, resp.Header, body)
	}
}
