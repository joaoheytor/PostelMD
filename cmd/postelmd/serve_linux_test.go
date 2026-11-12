//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/config"
	"github.com/joaoheytor/PostelMD/internal/store/lock"
)

// syncBuffer is a bytes.Buffer safe for the server goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type running struct {
	cfg    config.Config
	out    *syncBuffer
	stop   context.CancelFunc
	done   chan error
	origin string
}

func start(t *testing.T, data string) *running {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	r := &running{cfg: config.Config{DataDir: data, Listen: "127.0.0.1:0"}, out: &syncBuffer{}, stop: stop, done: make(chan error, 1)}
	addr := make(chan string, 1)
	go func() { r.done <- serve(ctx, r.cfg, r.out, func(a string) { addr <- a }) }()
	select {
	case a := <-addr:
		r.origin = "http://" + a
		r.cfg.Listen = a
	case err := <-r.done:
		stop()
		t.Fatalf("serve: %v", err)
	}
	t.Cleanup(func() { r.shutdown(t) })
	return r
}

func (r *running) shutdown(t *testing.T) {
	t.Helper()
	if r.stop == nil {
		return
	}
	r.stop()
	r.stop = nil
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("serve returned %v on shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve didn't shut down")
	}
}

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

func TestServeEndToEnd(t *testing.T) {
	data := t.TempDir()
	r := start(t, data)

	m := regexp.MustCompile(`one-time token\n\(valid 30m0s\): (\S+)`).FindStringSubmatch(r.out.String())
	if m == nil {
		t.Fatalf("no setup token printed:\n%s", r.out.String())
	}
	if strings.Contains(r.out.String(), `"`+m[1]+`"`) {
		t.Fatal("the setup token appeared in a structured log record")
	}
	if !strings.Contains(r.out.String(), " "+r.origin+"/setup ") || strings.Contains(r.out.String(), "/setup?") {
		t.Fatalf("the setup page's full URL, without the token, wasn't printed:\n%s", r.out.String())
	}
	// The operator's own log says which build runs (#152).
	if !regexp.MustCompile(`"msg":"listening",.*"version":"` + version + `","commit":"` + commit + `"`).MatchString(r.out.String()) {
		t.Fatalf("the listening line doesn't name the build:\n%s", r.out.String())
	}
	if resp := post(t, r.origin+"/api/v1/setup", `{"token":"`+m[1]+`","password":"correct horse battery","name":"Ada","email":"ada@example.invalid"}`); resp.StatusCode != 201 {
		t.Fatalf("setup = %d", resp.StatusCode)
	}
	if err := healthcheck(r.cfg); err != nil {
		t.Fatalf("healthcheck: %v", err)
	}
	// A name nobody configured is refused (DNS rebinding).
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, r.origin+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "rebind.example"
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("a request for an unknown host = %v, %v; want 421", resp, err)
	} else {
		_ = resp.Body.Close()
	}

	// One writer: a second server, or reset-password, on the same data is refused.
	var held *lock.HeldError
	if err := serve(context.Background(), config.Config{DataDir: data, Listen: "127.0.0.1:0"}, &syncBuffer{}, nil); !errors.As(err, &held) {
		t.Fatalf("a second server = %v, want HeldError", err)
	}
	if err := resetPassword(r.cfg, os.Stdin, &syncBuffer{}); !errors.As(err, &held) {
		t.Fatalf("reset-password next to a live server = %v, want HeldError", err)
	}

	r.shutdown(t)
	if err := healthcheck(r.cfg); err == nil {
		t.Fatal("healthcheck passed with the server stopped")
	}
	in, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString("a reset password here\n")
	_ = w.Close()
	if err := resetPassword(r.cfg, in, &syncBuffer{}); err != nil {
		t.Fatalf("reset-password: %v", err)
	}
	_ = in.Close()

	again := start(t, data)
	if strings.Contains(again.out.String(), "one-time token") {
		t.Fatal("a configured instance printed a setup token")
	}
	if resp := post(t, again.origin+"/api/v1/session", `{"password":"a reset password here"}`); resp.StatusCode != 204 {
		t.Fatalf("login with the reset password = %d", resp.StatusCode)
	}
	if resp := post(t, again.origin+"/api/v1/session", `{"password":"correct horse battery"}`); resp.StatusCode != 401 {
		t.Fatalf("login with the old password = %d, want 401", resp.StatusCode)
	}
}

func TestSetupURL(t *testing.T) {
	for _, tc := range []struct{ listen, actual, want string }{
		{":8080", "[::]:8080", "http://localhost:8080/setup"},
		{"0.0.0.0:8080", "0.0.0.0:8080", "http://localhost:8080/setup"},
		{"[::]:9000", "[::]:9000", "http://localhost:9000/setup"},
		{"127.0.0.1:0", "127.0.0.1:41234", "http://127.0.0.1:41234/setup"},
		{"[::1]:8080", "[::1]:8080", "http://[::1]:8080/setup"},
	} {
		if got := setupURL(tc.listen, tc.actual); got != tc.want {
			t.Errorf("setupURL(%q, %q) = %q, want %q", tc.listen, tc.actual, got, tc.want)
		}
	}
}

func TestServeRefusesNotesAtTheRoot(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(data+"/runbook.md", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := serve(context.Background(), config.Config{DataDir: data, Listen: "127.0.0.1:0"}, &syncBuffer{}, nil)
	if err == nil || !strings.Contains(err.Error(), "/data/vaults/notes") {
		t.Fatalf("serve with notes at the root = %v", err)
	}
}
