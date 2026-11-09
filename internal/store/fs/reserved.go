package fs

import (
	"strings"
	"unicode"
)

// reservedNames are path components that are reserved at any depth (ADR-0006, "Reserved
// paths"), compared case-insensitively. The trash and temp directories are only created at
// the vault root, but refusing them anywhere is simpler and loses nothing.
var reservedNames = map[string]bool{
	".git":            true,
	".postelmd":       true,
	".postelmd-trash": true,
	".postelmd-tmp":   true,
	".stfolder":       true,
	".stversions":     true,
}

// IsReserved reports whether a vault-relative path must never be reached through the API
// (ADR-0006). Callers apply it to every path a user supplies, for every operation, and
// answer 404 for a reserved one. Malformed paths count as reserved, so a path that passes
// is also a valid path for Root.
//
// `.postelmd-secrets-allow` is readable but writable only through the findings API, which
// arrives with the secret gates (ADR-0020); until then it is an ordinary file.
func IsReserved(p string) bool {
	if checkRel(p) != nil {
		return true
	}
	for _, c := range strings.Split(p, "/") {
		lc := strings.ToLower(c)
		if c == "" || c == "." || c == ".." || reservedNames[lc] || strings.Contains(lc, ".sync-conflict-") {
			return true
		}
		if strings.IndexFunc(c, unicode.IsControl) >= 0 {
			return true
		}
	}
	return false
}
