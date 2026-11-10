//go:build webui

package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built web app (`npm run build`), embedded in the binary.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // "dist" is a valid path, and the embed above guarantees it exists
	}
	return sub
}
