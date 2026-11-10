//go:build linux

package http

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

type trashList struct {
	Items     []vault.TrashItem `json:"items"`
	Truncated bool              `json:"truncated"`
}

func listTrash(t *testing.T, get func(c call) (int, string)) trashList {
	t.Helper()
	status, body := get(call{method: "GET", path: "/api/v1/trash"})
	var l trashList
	if status != 200 || json.Unmarshal([]byte(body), &l) != nil {
		t.Fatalf("GET /api/v1/trash: %d %s", status, body)
	}
	return l
}

// TestTrashAPI: a deleted note is listed and restored, with plain errors for a clash, a
// missing folder, a long name and an id that isn't a trash entry; every route needs a
// session.
func TestTrashAPI(t *testing.T) {
	srv, svc := newServer(t, false)
	cookie := setup(t, srv, svc)
	for _, c := range []call{
		{method: "GET", path: "/api/v1/trash"},
		{method: "POST", path: "/api/v1/trash/x/restore", header: jsonHeader, body: `{}`},
	} {
		if resp, _ := do(t, srv, c); resp.StatusCode != 401 {
			t.Fatalf("%s %s without a session: %d, want 401", c.method, c.path, resp.StatusCode)
		}
	}
	as := func(c call) (int, string) {
		t.Helper()
		c.header = withCookie(cookie, c.header)
		resp, body := do(t, srv, c)
		return resp.StatusCode, body
	}

	if l := listTrash(t, as); len(l.Items) != 0 || l.Truncated || !strings.Contains(mustGet(t, as, "/api/v1/trash"), `"items":[]`) {
		t.Fatalf("empty trash listed %+v", l)
	}
	if status, body := as(call{method: "DELETE", path: "/api/v1/notes/a.md"}); status != 200 {
		t.Fatalf("delete: %d %s", status, body)
	}
	l := listTrash(t, as)
	if len(l.Items) != 1 || l.Items[0].Name != "a.md" || l.Items[0].Kind != vault.KindFile || l.Items[0].Size == nil || *l.Items[0].Size != 1 {
		t.Fatalf("trash = %+v", l)
	}
	id := url.PathEscape(l.Items[0].ID)
	restore := func(body string) (int, string) {
		return as(call{method: "POST", path: "/api/v1/trash/" + id + "/restore", header: jsonHeader, body: body})
	}

	for _, tc := range []struct {
		name, path, body string
		status           int
		msg              string
	}{
		{"not a trash entry", "/api/v1/trash/a.md/restore", `{}`, 410, "no longer in the trash"},
		{"a path into the trash", "/api/v1/trash/" + id + "%2Fa.md/restore", `{}`, 410, "no longer in the trash"},
		{"out of the trash", "/api/v1/trash/..%2F..%2Fa.md/restore", `{}`, 410, ""},
		{"a path as the name", "", `{"name":"notes/a.md"}`, 400, "a name can't contain /"},
		{"missing folder", "", `{"folder":"nope"}`, 404, "folder doesn't exist"},
		{"into .git", "", `{"folder":".git"}`, 404, ""},
		{"into the trash", "", `{"folder":".postelmd-trash"}`, 404, ""},
		{"long name", "", `{"name":"` + strings.Repeat("x", 256) + `"}`, 400, "at most 255 bytes"},
		{"unknown field", "", `{"to":"x"}`, 400, ""},
	} {
		p := tc.path
		if p == "" {
			p = "/api/v1/trash/" + id + "/restore"
		}
		status, body := as(call{method: "POST", path: p, header: jsonHeader, body: tc.body})
		if status != tc.status || !strings.Contains(body, tc.msg) {
			t.Errorf("%s: %d %s, want %d %q", tc.name, status, body, tc.status, tc.msg)
		}
	}

	// A clash: something new took the name meanwhile.
	if status, _ := as(call{method: "PUT", path: "/api/v1/notes/a.md", body: "new", header: map[string]string{"If-None-Match": "*"}}); status != 201 {
		t.Fatalf("create: %d", status)
	}
	if status, body := restore(`{}`); status != 409 || !strings.Contains(body, "choose another name") {
		t.Fatalf("restore over a.md: %d %s, want 409", status, body)
	}
	if status, body := restore(`{"folder":"","name":"a (restored).md"}`); status != 200 || !strings.Contains(body, `"path":"a (restored).md"`) {
		t.Fatalf("restore under another name: %d %s", status, body)
	}
	if status, body := as(call{method: "GET", path: "/api/v1/notes/a%20(restored).md"}); status != 200 || body != "A" {
		t.Fatalf("restored note: %d %q", status, body)
	}
	if l := listTrash(t, as); len(l.Items) != 0 {
		t.Fatalf("still listed: %+v", l)
	}
	if status, body := restore(`{}`); status != 410 {
		t.Fatalf("restore again: %d %s, want 410", status, body)
	}
	// The restored file is a change for git, like any new file.
	if body := mustGet(t, as, "/api/v1/git/status"); !strings.Contains(body, `"path":"a (restored).md"`) {
		t.Fatalf("git status doesn't show the restored note: %s", body)
	}
}

func mustGet(t *testing.T, as func(c call) (int, string), p string) string {
	t.Helper()
	status, body := as(call{method: "GET", path: p})
	if status != 200 {
		t.Fatalf("GET %s: %d %s", p, status, body)
	}
	return body
}
