package http

import (
	"context"
	"log/slog"
	stdhttp "net/http"
	"strings"
	"time"
)

// Write logging (ADR-0020's success criteria: zero 5xx on saves, every 412 resolved). Every
// mutating API request gets one log line with its method, route pattern, status and
// duration. Only the pattern is logged ("PUT /api/v1/notes/{path...}"), never the concrete
// path: note paths are user content (CLAUDE.md rule 15), and so are bodies, headers and
// cookies, none of which is logged. Reads stay quiet.

// route records the most specific pattern a request matched, across the nested muxes.
type route struct{ pattern string }

type routeKey struct{}

// routed records, before serving, the pattern mux matches for r. Each nested mux is
// wrapped, so the innermost match wins.
func routed(mux *stdhttp.ServeMux) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if rt, ok := r.Context().Value(routeKey{}).(*route); ok {
			if _, p := mux.Handler(r); p != "" {
				rt.pattern = p
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// statusWriter keeps the status a handler sent.
type statusWriter struct {
	stdhttp.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = stdhttp.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusWriter) Unwrap() stdhttp.ResponseWriter { return s.ResponseWriter }

// mutating tells the requests logWrites logs: a non-safe method under /api/v1/.
func mutating(r *stdhttp.Request) bool {
	switch r.Method {
	case stdhttp.MethodPost, stdhttp.MethodPut, stdhttp.MethodDelete, stdhttp.MethodPatch:
		return strings.HasPrefix(r.URL.Path, "/api/v1/")
	}
	return false
}

// logWrites logs one line per mutating API request: Info below 400, Warn for 4xx, Error
// for 5xx. A request that matched no route (refused before routing, e.g. cross-origin or
// before setup) is logged with route "-".
func logWrites(log *slog.Logger, next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if !mutating(r) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rt := &route{pattern: "-"}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), routeKey{}, rt)))
		status := sw.status
		if status == 0 {
			status = stdhttp.StatusOK // nothing written: net/http sends 200
		}
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}
		log.LogAttrs(r.Context(), level, "request",
			slog.String("method", r.Method),
			slog.String("route", rt.pattern),
			slog.Int("status", status),
			slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))
	})
}
