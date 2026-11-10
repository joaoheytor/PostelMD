package http

import (
	"log/slog"
	stdhttp "net/http"
	"strings"
)

// NewRouter assembles the whole HTTP surface (ADR-0020): health checks, the auth API, and
// the notes API behind a session. Around it, from the outside in:
//
//   - security headers on every response;
//   - cross-origin protection: a non-safe request from another origin is refused (403),
//     so a site the user visits can't drive the API with their cookie;
//   - the pre-setup gate (ADR-0009): until setup, only setup and health are served.
func NewRouter(a Auth, notes Notes, log *slog.Logger) stdhttp.Handler {
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)
	(&AuthAPI{auth: a, log: log}).register(mux)
	notesAPI := RequireSession(a, NewNotesAPI(notes, log))
	for _, prefix := range []string{"/api/v1/notes/", "/api/v1/folders/", "/api/v1/move"} {
		mux.Handle(prefix, notesAPI)
	}
	mux.HandleFunc("/api/", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeError(w, stdhttp.StatusNotFound, "not found")
	})

	return securityHeaders(stdhttp.NewCrossOriginProtection().Handler(setupGate(a, mux)))
}

func ok(w stdhttp.ResponseWriter, _ *stdhttp.Request) { w.WriteHeader(stdhttp.StatusNoContent) }

// setupGate implements ADR-0009's pre-setup routing: setup and health are served, any other
// API path is 503, and a page navigation is redirected to /setup.
func setupGate(a Auth, next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if !a.NeedsSetup() {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		switch {
		case p == "/api/v1/setup", p == "/healthz", p == "/readyz", p == "/setup", strings.HasPrefix(p, "/assets/"):
			next.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/api/"):
			writeError(w, stdhttp.StatusServiceUnavailable, "setup required")
		case p == "/metrics":
			stdhttp.NotFound(w, r)
		default:
			stdhttp.Redirect(w, r, "/setup", stdhttp.StatusSeeOther)
		}
	})
}

// securityHeaders sets the headers every response carries. Handlers may tighten them (a
// note's CSP is "sandbox").
func securityHeaders(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}
