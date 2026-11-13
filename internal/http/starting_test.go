package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
)

// TestStarting: while the server starts (ADR-0030, startup order step 1), /healthz answers
// 2xx and /readyz and every other route 503 with Retry-After, with the security headers;
// once Ready installs the router, it serves everything.
func TestStarting(t *testing.T) {
	var s Starting
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(stdhttp.MethodGet, path, nil))
		return w
	}
	if w := get("/healthz"); w.Code != stdhttp.StatusOK {
		t.Fatalf("/healthz while starting = %d, want 200 (ADR-0030)", w.Code)
	}
	for _, p := range []string{"/readyz", "/api/v1/about", "/api/v1/setup", "/", "/setup", "/assets/x.js"} {
		w := get(p)
		if w.Code != stdhttp.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
			t.Fatalf("%s while starting = %d (Retry-After %q), want 503 with Retry-After", p, w.Code, w.Header().Get("Retry-After"))
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("%s while starting lacks the security headers: %v", p, w.Header())
		}
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(stdhttp.MethodPost, "/healthz", nil))
	if w.Code != stdhttp.StatusServiceUnavailable {
		t.Fatalf("POST /healthz while starting = %d, want 503", w.Code)
	}

	s.Ready(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) { w.WriteHeader(stdhttp.StatusTeapot) }))
	for _, p := range []string{"/healthz", "/readyz", "/api/v1/about"} {
		if w := get(p); w.Code != stdhttp.StatusTeapot {
			t.Fatalf("%s after Ready = %d, want the router's answer", p, w.Code)
		}
	}
}
