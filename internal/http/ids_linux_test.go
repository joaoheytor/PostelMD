//go:build linux

package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

// fakeIDs answers Seen with a fixed wait or error and records the paths it was asked about.
type fakeIDs struct {
	wait time.Duration
	err  error
	seen []string
}

func (f *fakeIDs) Seen(_ context.Context, p string) (time.Duration, error) {
	f.seen = append(f.seen, p)
	return f.wait, f.err
}

// A read of a path held for a rename match answers 503 with Retry-After in whole seconds,
// rounded up (ADR-0031); otherwise, and when Seen fails, the read is served.
func TestReadOfAHeldPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	for _, c := range []struct {
		name   string
		ids    *fakeIDs
		status int
		retry  string
	}{
		{"held", &fakeIDs{wait: 6*time.Second + time.Millisecond}, stdhttp.StatusServiceUnavailable, "7"},
		{"held, under a second left", &fakeIDs{wait: time.Millisecond}, stdhttp.StatusServiceUnavailable, "1"},
		{"not held", &fakeIDs{}, stdhttp.StatusOK, ""},
		{"Seen failed", &fakeIDs{err: errors.New("disk I/O error at /data/vaults/notes/a.md")}, stdhttp.StatusOK, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var logs strings.Builder
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			srv := httptest.NewServer((&NotesAPI{notes: v, ids: c.ids, log: log}).mux())
			defer srv.Close()
			resp, err := stdhttp.Get(srv.URL + "/api/v1/notes/a.md")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != c.status || resp.Header.Get("Retry-After") != c.retry {
				t.Fatalf("GET = %d, Retry-After %q, %s; want %d, %q", resp.StatusCode, resp.Header.Get("Retry-After"), body, c.status, c.retry)
			}
			if c.status == stdhttp.StatusOK && string(body) != "A" {
				t.Fatalf("body %q", body)
			}
			if len(c.ids.seen) != 1 || c.ids.seen[0] != "a.md" {
				t.Fatalf("Seen asked about %q", c.ids.seen)
			}
			if strings.Contains(logs.String(), "a.md") {
				t.Fatalf("a log record names the path: %s", logs.String())
			}
		})
	}
}
