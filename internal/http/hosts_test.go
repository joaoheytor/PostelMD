package http

import (
	"bytes"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A page on another site can rebind its own name to this server's address; the browser then
// sends that name as Host. Only names the operator configured, localhost and IP literals
// reach the app.
func TestAllowHosts(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	h := AllowHosts([]string{"notes.example.org"}, log, stdhttp.HandlerFunc(ok))
	for _, tc := range []struct {
		host string
		want int
	}{
		{"localhost", 204},
		{"localhost:8080", 204},
		{"LOCALHOST.:8080", 204},
		{"app.localhost:8080", 204},
		{"127.0.0.1:8080", 204},
		{"10.1.2.3", 204},
		{"[::1]:8080", 204},
		{"[::1]", 204},
		{"[fe80::1%25eth0]:8080", 204},
		{"notes.example.org", 204},
		{"Notes.Example.Org:443", 204},
		{"notes.example.org.", 204},
		{"rebind.attacker.example", 421},
		{"rebind.attacker.example:8080", 421},
		{"example.org", 421},
		{"evil-notes.example.org", 421},
		{"localhost.attacker.example", 421},
		{"", 421},
	} {
		r := httptest.NewRequest("GET", "/api/v1/files", nil)
		r.Host = tc.host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("Host %q = %d, want %d", tc.host, w.Code, tc.want)
		}
		if tc.want == 421 && !strings.Contains(w.Body.String(), "allowed_hosts") {
			t.Errorf("Host %q: the refusal doesn't name the setting: %q", tc.host, w.Body.String())
		}
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "allowed_hosts") {
		t.Fatalf("no warning naming the setting:\n%s", logs.String())
	}
}

// The refused Host is the client's to choose: it reaches the log escaped and truncated.
func TestAllowHostsLogsASafeHost(t *testing.T) {
	var logs bytes.Buffer
	h := AllowHosts(nil, slog.New(slog.NewJSONHandler(&logs, nil)), stdhttp.HandlerFunc(ok))
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "evil\n" + strings.Repeat("x", 500) + ".example"
	h.ServeHTTP(httptest.NewRecorder(), r)
	if strings.Count(logs.String(), "\n") != 1 || strings.Contains(logs.String(), strings.Repeat("x", 100)) {
		t.Fatalf("the Host reached the log raw or whole:\n%s", logs.String())
	}
}
