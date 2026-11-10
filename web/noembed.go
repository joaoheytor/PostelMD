//go:build !webui

package web

import "io/fs"

// Dist returns nil: this binary was built without the web UI.
func Dist() fs.FS { return nil }
