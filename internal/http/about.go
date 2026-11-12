package http

import stdhttp "net/http"

// Build identifies the running binary: the version and commit the build stamped into it.
type Build struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// about serves GET /api/v1/about: {version, commit}, behind a session only (#152). An exact
// version tells an attacker which known bugs to try, so nothing served before login names
// it: not the login or setup page, not a header, not the health checks. (The hashed asset
// names still identify a public build: accepted, threat model I14.)
func about(b Build) stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		writeJSON(w, stdhttp.StatusOK, b)
	}
}
