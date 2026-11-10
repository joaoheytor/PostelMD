//go:build linux

package http

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

// lockedBuffer is a bytes.Buffer safe for the server's goroutines and the test.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// requestLines parses the "request" lines of a JSON log.
func requestLines(t *testing.T, log string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a log line isn't JSON: %q", line)
		}
		if m["msg"] == "request" {
			out = append(out, m)
		}
	}
	return out
}

// TestEveryWriteIsLogged: each mutating API request leaves one line with its method, route
// pattern, status and duration, whatever the outcome (ADR-0020's success criteria count 5xx
// and 412s); reads leave none; and nothing from the request (the note's path, its content,
// headers, cookies, the password) reaches the log (CLAUDE.md rule 15).
func TestEveryWriteIsLogged(t *testing.T) {
	buf := &lockedBuffer{}
	srv, svc := newLoggingServer(t, false, slog.New(slog.NewJSONHandler(buf, nil)))
	cookie := setup(t, srv, svc)
	// Built at run time, so none of these literals is in the log by accident.
	secretName, secretBody := "incident-"+strings.Repeat("q7", 4)+".md", "body-"+strings.Repeat("z9", 4)
	stale := etag(vault.RevOf([]byte("old")))
	auth := func(h map[string]string) map[string]string { return withCookie(cookie, h) }

	tests := []struct {
		name   string
		call   call
		logged bool
		status int
		route  string
		level  string
	}{
		{"create", call{method: "PUT", path: "/api/v1/notes/" + secretName, body: secretBody,
			header: auth(map[string]string{"If-None-Match": "*"})}, true, 201, "PUT /api/v1/notes/{path...}", "INFO"},
		{"stale save", call{method: "PUT", path: "/api/v1/notes/" + secretName, body: secretBody,
			header: auth(map[string]string{"If-Match": stale})}, true, 412, "PUT /api/v1/notes/{path...}", "WARN"},
		{"read", call{method: "GET", path: "/api/v1/notes/" + secretName, header: auth(nil)}, false, 200, "", ""},
		{"history", call{method: "GET", path: "/api/v1/history/" + secretName, header: auth(nil)}, false, 200, "", ""},
		{"mkdir", call{method: "POST", path: "/api/v1/folders/" + secretName + "-dir", header: auth(nil)},
			true, 201, "POST /api/v1/folders/{path...}", "INFO"},
		{"commit", call{method: "POST", path: "/api/v1/git/commit", body: `{"message":"` + secretBody +
			`","changes":[{"path":"` + secretName + `","rev":"` + string(vault.RevOf([]byte(secretBody))) + `"}]}`,
			header: auth(jsonHeader)}, true, 201, "POST /api/v1/git/commit", "INFO"},
		{"no session", call{method: "DELETE", path: "/api/v1/notes/" + secretName}, true, 401, "/api/v1/notes/", "WARN"},
		{"wrong password", call{method: "POST", path: "/api/v1/session", header: jsonHeader,
			body: `{"password":"` + secretBody + `"}`}, true, 401, "POST /api/v1/session", "WARN"},
		{"cross-site", call{method: "DELETE", path: "/api/v1/notes/" + secretName,
			header: auth(map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"})}, true, 403, "-", "WARN"},
		{"delete", call{method: "DELETE", path: "/api/v1/notes/" + secretName, header: auth(nil)},
			true, 200, "DELETE /api/v1/notes/{path...}", "INFO"},
		{"trash", call{method: "GET", path: "/api/v1/trash", header: auth(nil)}, false, 200, "", ""},
		{"restore elsewhere", call{method: "POST", path: "/api/v1/trash/20261110T000000.000000000Z-" + secretName + "/restore",
			body: `{"name":"` + secretBody + `"}`, header: auth(jsonHeader)}, true, 410, "POST /api/v1/trash/{id}/restore", "WARN"},
		{"unknown API", call{method: "POST", path: "/api/v1/" + secretName, header: auth(nil)}, true, 404, "/api/", "WARN"},
	}
	for _, tt := range tests {
		before := len(requestLines(t, buf.String()))
		resp, body := do(t, srv, tt.call)
		if resp.StatusCode != tt.status {
			t.Fatalf("%s: %d %s, want %d", tt.name, resp.StatusCode, body, tt.status)
		}
		var lines []map[string]any
		for deadline := time.Now().Add(2 * time.Second); ; {
			lines = requestLines(t, buf.String())[before:]
			if len(lines) > 0 || !tt.logged || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if !tt.logged {
			if len(lines) != 0 {
				t.Errorf("%s: a read was logged: %v", tt.name, lines)
			}
			continue
		}
		if len(lines) != 1 {
			t.Fatalf("%s: %d request lines, want 1: %v", tt.name, len(lines), lines)
		}
		l := lines[0]
		d, isNum := l["duration_ms"].(float64)
		if l["method"] != tt.call.method || l["route"] != tt.route || l["status"] != float64(tt.status) ||
			l["level"] != tt.level || !isNum || d < 0 {
			t.Errorf("%s: logged %v, want %s %q %d at %s with a duration", tt.name, l, tt.call.method, tt.route, tt.status, tt.level)
		}
		if len(l) != 7 { // time, level, msg, method, route, status, duration_ms: nothing else
			t.Errorf("%s: unexpected fields in %v", tt.name, l)
		}
	}

	log := buf.String()
	for _, leak := range []string{"incident-", secretBody, cookie.Value, "evil.example", "If-None-Match", stale} {
		if strings.Contains(log, leak) {
			t.Errorf("the log holds %q:\n%s", leak, log)
		}
	}
}
