package http

import (
	stdhttp "net/http"
	"sync/atomic"
)

// Starting is the HTTP surface while the server starts (ADR-0030, startup order step 1): the
// listener is up right after the instance lock, so orchestrators see a live process during a
// long start (a first upgrade on a large vault). Until Ready installs the router, GET or HEAD
// /healthz answers 204, and /readyz and every other route answer 503 with Retry-After,
// with the usual security headers. It serves nothing else.
type Starting struct {
	router atomic.Pointer[stdhttp.Handler]
}

// startingRetryAfter is the Retry-After, in seconds, of a request made while starting.
const startingRetryAfter = "5"

var starting = securityHeaders(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/healthz" && (r.Method == stdhttp.MethodGet || r.Method == stdhttp.MethodHead) {
		ok(w, r)
		return
	}
	w.Header().Set("Retry-After", startingRetryAfter)
	writeError(w, stdhttp.StatusServiceUnavailable, "starting")
}))

// Ready installs the router: from then on, every request goes to it.
func (s *Starting) Ready(router stdhttp.Handler) { s.router.Store(&router) }

func (s *Starting) ServeHTTP(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if h := s.router.Load(); h != nil {
		(*h).ServeHTTP(w, r)
		return
	}
	starting.ServeHTTP(w, r)
}
