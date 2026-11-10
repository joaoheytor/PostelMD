//go:build linux

package http

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	stdhttp "net/http"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

// listedNow returns the changes the status lists now, as the commit dialog sends them back.
func listedNow(t *testing.T, srv *httptest.Server, cookie *stdhttp.Cookie) []vault.Listed {
	t.Helper()
	resp, body := do(t, srv, call{method: "GET", path: "/api/v1/git/status", header: withCookie(cookie, nil)})
	var st struct{ Changes []vault.Listed }
	if err := json.Unmarshal([]byte(body), &st); resp.StatusCode != 200 || err != nil {
		t.Fatalf("status = %d %s (%v)", resp.StatusCode, body, err)
	}
	return st.Changes
}

func bodyOf(t *testing.T, msg string, listed []vault.Listed) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"message": msg, "changes": listed})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// commitBody is the body of a commit of every change the status lists now.
func commitBody(t *testing.T, srv *httptest.Server, cookie *stdhttp.Cookie, msg string) string {
	t.Helper()
	return bodyOf(t, msg, listedNow(t, srv, cookie))
}

func historyLen(t *testing.T, srv *httptest.Server, cookie *stdhttp.Cookie, p string) int {
	t.Helper()
	_, body := do(t, srv, call{method: "GET", path: "/api/v1/history/" + p, header: withCookie(cookie, nil)})
	var h struct{ Versions []json.RawMessage }
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatalf("history = %s (%v)", body, err)
	}
	return len(h.Versions)
}

// TestCommitBindsToTheListedChanges: the server commits only the set the dialog listed;
// any change since then answers 409 with a code the dialog recognizes, and commits nothing.
func TestCommitBindsToTheListedChanges(t *testing.T) {
	cases := []struct {
		name string
		edit func(t *testing.T, srv *httptest.Server, cookie *stdhttp.Cookie, l []vault.Listed) []vault.Listed
	}{
		{"a listed file saved again", func(t *testing.T, srv *httptest.Server, cookie *stdhttp.Cookie, l []vault.Listed) []vault.Listed {
			_, body := do(t, srv, call{method: "GET", path: "/api/v1/notes/b.md", header: withCookie(cookie, nil)})
			save(t, srv, cookie, "b.md", body+" more")
			return l
		}},
		{"an untracked file created", func(t *testing.T, srv *httptest.Server, cookie *stdhttp.Cookie, l []vault.Listed) []vault.Listed {
			create(t, srv, cookie, "new.md", "N")
			return l
		}},
		{"a listed file trashed", func(t *testing.T, srv *httptest.Server, cookie *stdhttp.Cookie, l []vault.Listed) []vault.Listed {
			if resp, _ := do(t, srv, call{method: "DELETE", path: "/api/v1/notes/b.md", header: withCookie(cookie, nil)}); resp.StatusCode != 200 {
				t.Fatalf("delete = %d", resp.StatusCode)
			}
			return l
		}},
		{"a deletion left out", func(_ *testing.T, _ *httptest.Server, _ *stdhttp.Cookie, l []vault.Listed) []vault.Listed {
			var out []vault.Listed
			for _, c := range l {
				if c.Blob != nil {
					out = append(out, c)
				}
			}
			return out
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, svc := newServer(t, false)
			cookie := setup(t, srv, svc)
			create(t, srv, cookie, "b.md", "B")
			if resp, _ := do(t, srv, call{method: "DELETE", path: "/api/v1/notes/a.md", header: withCookie(cookie, nil)}); resp.StatusCode != 200 {
				t.Fatalf("delete a.md = %d", resp.StatusCode)
			}
			l := tc.edit(t, srv, cookie, listedNow(t, srv, cookie))
			resp, body := do(t, srv, call{method: "POST", path: "/api/v1/git/commit", header: withCookie(cookie, jsonHeader), body: bodyOf(t, "stale", l)})
			if resp.StatusCode != 409 || !strings.Contains(body, `"code":"changes-differ"`) {
				t.Fatalf("commit = %d %s, want 409 changes-differ", resp.StatusCode, body)
			}
			if n := historyLen(t, srv, cookie, "a.md"); n != 1 {
				t.Fatalf("a.md has %d versions; the stale commit landed", n)
			}
			if len(listedNow(t, srv, cookie)) == 0 {
				t.Fatal("the changes were committed")
			}
		})
	}
}

// TestCommitNeedsTheListedChanges: the web app is the only client, and it always sends what
// it listed; a request without the list (the old, message-only form) is refused.
func TestCommitNeedsTheListedChanges(t *testing.T) {
	srv, svc := newServer(t, false)
	cookie := setup(t, srv, svc)
	save(t, srv, cookie, "a.md", "A2")
	resp, body := do(t, srv, call{method: "POST", path: "/api/v1/git/commit", header: withCookie(cookie, jsonHeader), body: `{"message":"Edit a"}`})
	if resp.StatusCode != 422 || !strings.Contains(body, "changes") {
		t.Fatalf("message-only commit = %d %s, want 422", resp.StatusCode, body)
	}
	if n := historyLen(t, srv, cookie, "a.md"); n != 1 {
		t.Fatalf("a.md has %d versions, want 1", n)
	}
}

// TestCommitOfManyChanges: the list of a large change set fits the commit's body limit.
func TestCommitOfManyChanges(t *testing.T) {
	srv, svc := newServer(t, false)
	cookie := setup(t, srv, svc)
	for i := range 120 {
		create(t, srv, cookie, fmt.Sprintf("runbook-%s-%03d.md", strings.Repeat("x", 80), i), "R")
	}
	body := commitBody(t, srv, cookie, "Add runbooks")
	if len(body) <= 8<<10 {
		t.Fatalf("the body is %d bytes; the test needs more than the default 8 KiB", len(body))
	}
	if resp, b := do(t, srv, call{method: "POST", path: "/api/v1/git/commit", header: withCookie(cookie, jsonHeader), body: body}); resp.StatusCode != 201 {
		t.Fatalf("commit = %d %s", resp.StatusCode, b)
	}
}

// TestConcurrentCommitsOfOneList: the vault mutex serializes them; one commits the list,
// and the others find it no longer matches.
func TestConcurrentCommitsOfOneList(t *testing.T) {
	srv, svc := newServer(t, false)
	cookie := setup(t, srv, svc)
	save(t, srv, cookie, "a.md", "A2")
	body := commitBody(t, srv, cookie, "Edit a")
	codes := make(chan int, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			resp, _ := do(t, srv, call{method: "POST", path: "/api/v1/git/commit", header: withCookie(cookie, jsonHeader), body: body})
			codes <- resp.StatusCode
		})
	}
	wg.Wait()
	close(codes)
	count := map[int]int{}
	for c := range codes {
		count[c]++
	}
	if count[201] != 1 || count[409] != 7 {
		t.Fatalf("status codes = %v, want one 201 and seven 409", count)
	}
	if n := historyLen(t, srv, cookie, "a.md"); n != 2 {
		t.Fatalf("a.md has %d versions, want 2", n)
	}
}

func save(t *testing.T, srv *httptest.Server, cookie *stdhttp.Cookie, p, content string) {
	t.Helper()
	resp, _ := do(t, srv, call{method: "GET", path: "/api/v1/notes/" + p, header: withCookie(cookie, nil)})
	h := withCookie(cookie, map[string]string{"If-Match": resp.Header.Get("ETag")})
	if resp, body := do(t, srv, call{method: "PUT", path: "/api/v1/notes/" + p, header: h, body: content}); resp.StatusCode != 204 {
		t.Fatalf("save %s = %d %s", p, resp.StatusCode, body)
	}
}

func create(t *testing.T, srv *httptest.Server, cookie *stdhttp.Cookie, p, content string) {
	t.Helper()
	h := withCookie(cookie, map[string]string{"If-None-Match": "*"})
	if resp, body := do(t, srv, call{method: "PUT", path: "/api/v1/notes/" + p, header: h, body: content}); resp.StatusCode != 201 {
		t.Fatalf("create %s = %d %s", p, resp.StatusCode, body)
	}
}
