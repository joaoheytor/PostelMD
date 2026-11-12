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
//   - one log line per mutating API request (route pattern, status, duration; logWrites);
//   - security headers on every response;
//   - cross-origin protection: a non-safe request from another origin is refused (403),
//     so a site the user visits can't drive the API with their cookie;
//   - the pre-setup gate (ADR-0009): until setup, only setup and health are served.
//
// ui is the built web app, served outside /api/ (nil when the binary has no UI); build is
// what /api/v1/about tells a logged-in user.
func NewRouter(a Auth, v Vault, ui fs.FS, build Build, log *slog.Logger) stdhttp.Handler {
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)
	(&AuthAPI{auth: a, importer: v, log: log}).register(mux)
	notesAPI := routed(NewNotesAPI(v, log))
	private := stdhttp.NewServeMux()
	private.Handle("/api/v1/notes/", notesAPI)
	private.Handle("/api/v1/folders/", notesAPI)
	private.Handle("/api/v1/move", notesAPI)
	private.Handle("/api/v1/files", notesAPI)
	errs := &NotesAPI{notes: v, log: log}
	(&GitAPI{git: v, author: a, notes: errs, log: log}).register(private)
	(&TrashAPI{trash: v, notes: errs, log: log}).register(private)
	private.HandleFunc("GET /api/v1/about", about(build))
	for _, prefix := range []string{"/api/v1/notes/", "/api/v1/folders/", "/api/v1/move", "/api/v1/files", "/api/v1/git/",
		"/api/v1/history/", "/api/v1/versions/", "/api/v1/restore", "/api/v1/trash", "/api/v1/trash/", "/api/v1/about"} {
		mux.Handle(prefix, RequireSession(a, routed(private)))
	}
	mux.HandleFunc("/api/", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeError(w, stdhttp.StatusNotFound, "not found")
	})
	mux.Handle("/", webApp(ui))

	return logWrites(log, securityHeaders(stdhttp.NewCrossOriginProtection().Handler(setupGate(a, routed(mux)))))
}

// Vault is everything the API needs from the vault. *vault.Vault implements it.
type Vault interface {
	Notes
	Git
	Trash
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

// csp is the web app's Content Security Policy (ADR-0007, ADR-0019): scripts, styles and
// Workers only from our origin, with no inline script or style; and Trusted Types, so HTML
// reaches the page only through the preview's sanitizing policy. The other two names are
// DOMPurify's internal policy and Lit's, which only ever sees Lit's own template strings.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; worker-src 'self'; " +
	"img-src 'self' blob: data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; " +
	"require-trusted-types-for 'script'; trusted-types postelmd-preview dompurify lit-html"

// securityHeaders sets the headers every response carries. Handlers may tighten them (a
// note's CSP is "sandbox").
func securityHeaders(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", csp)
		// No other origin may hold a window reference to the app, or embed its responses.
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
