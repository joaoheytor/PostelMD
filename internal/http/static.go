package http

import (
	"io/fs"
	stdhttp "net/http"
	"strings"
)

// webApp serves the embedded single-page app (ui is nil when the binary was built without
// it):
//
//	GET /assets/…  the built files; their names carry a content hash, so they are immutable
//	GET /frames/diagram, /frames/diagram.js  the diagram renderer frame, under its own CSP
//	GET anything else outside /api/: index.html, which routes on the client
func webApp(ui fs.FS) stdhttp.Handler {
	if ui == nil {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(stdhttp.StatusNotFound)
			_, _ = w.Write([]byte("This postelmd binary was built without the web UI. Build it with `make build`.\n"))
		})
	}
	assets := stdhttp.FileServerFS(ui)
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.Method != stdhttp.MethodGet && r.Method != stdhttp.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, stdhttp.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			if strings.HasSuffix(r.URL.Path, "/") { // no directory listings
				stdhttp.NotFound(w, r)
				return
			}
			if _, err := fs.Stat(ui, strings.TrimPrefix(r.URL.Path, "/")); err == nil {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			assets.ServeHTTP(w, r)
			return
		}
		if file, ok := frameFiles[r.URL.Path]; ok {
			serveFrame(w, r, ui, file)
			return
		}
		// Every other page is the app's shell. It names the current assets, so it is never
		// cached without checking.
		w.Header().Set("Cache-Control", "no-cache")
		stdhttp.ServeFileFS(w, r, ui, "index.html")
	})
}

// frameFiles are the diagram renderer frame's page and script (ADR-0021). The script's name
// has no content hash, so both are revalidated.
var frameFiles = map[string]string{
	"/frames/diagram":    "frames/diagram.html",
	"/frames/diagram.js": "frames/diagram.js",
}

// diagramCSP is the renderer frame's policy (ADR-0021). The sandbox directive gives it an
// opaque origin even when it is opened on its own, so Mermaid's raw HTML and inline styles
// can't reach the app, its cookies or its API; default-src 'none' leaves it no network.
const diagramCSP = "sandbox allow-scripts; default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; " +
	"img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

func serveFrame(w stdhttp.ResponseWriter, r *stdhttp.Request, ui fs.FS, file string) {
	h := w.Header()
	h.Set("Content-Security-Policy", diagramCSP)
	h.Set("Cache-Control", "no-cache")
	if strings.HasSuffix(file, ".html") {
		h.Set("X-Frame-Options", "SAMEORIGIN") // the app's pages frame it; nobody else
	}
	stdhttp.ServeFileFS(w, r, ui, file)
}
