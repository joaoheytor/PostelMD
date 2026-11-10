// Package web embeds the built single-page app into the binary. `make build` builds it
// first and sets the webui build tag; a plain `go build` or `go test` doesn't need Node.js,
// and that binary serves the API with a page saying the UI wasn't built.
package web
