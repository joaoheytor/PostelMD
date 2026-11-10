package http

import (
	"io/fs"
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
//
// ui is the built web app, served outside /api/ (nil when the binary has no UI).
func NewRouter(a Auth, v Vault, ui fs.FS, log *slog.Logger) stdhttp.Handler {
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)
	(&AuthAPI{auth: a, importer: v, log: log}).register(mux)
	notesAPI := NewNotesAPI(v, log)
	private := stdhttp.NewServeMux()
	private.Handle("/api/v1/notes/", notesAPI)
	private.Handle("/api/v1/folders/", notesAPI)
	private.Handle("/api/v1/move", notesAPI)
	(&GitAPI{git: v, author: a, notes: &NotesAPI{notes: v, log: log}, log: log}).register(private)
	for _, prefix := range []string{"/api/v1/notes/", "/api/v1/folders/", "/api/v1/move", "/api/v1/git/",
		"/api/v1/history/", "/api/v1/versions/", "/api/v1/restore"} {
		mux.Handle(prefix, RequireSession(a, private))
	}
	mux.HandleFunc("/api/", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeError(w, stdhttp.StatusNotFound, "not found")
	})
	mux.Handle("/", webApp(ui))

	return securityHeaders(stdhttp.NewCrossOriginProtection().Handler(setupGate(a, mux)))
}

// Vault is everything the API needs from the vault. *vault.Vault implements it.
type Vault interface {
	Notes
	Git
	Importer
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
		case p == "/api/v1/setup", p == "/api/v1/setup/check", p == "/healthz", p == "/readyz", p == "/setup", strings.HasPrefix(p, "/assets/"):
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
