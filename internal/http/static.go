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
		// Every other page is the app's shell. It names the current assets, so it is never
		// cached without checking.
		w.Header().Set("Cache-Control", "no-cache")
		stdhttp.ServeFileFS(w, r, ui, "index.html")
	})
}
