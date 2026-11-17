package http

import (
	stdhttp "net/http"
)

// ReadOnly is what /readyz needs from the vault: whether it went read-only after a storage
// fault (ADR-0030, ADR-0037). *vault.Vault implements it.
type ReadOnly interface {
	ReadOnly() bool
}

// readOnlyMessage answers a change to a read-only vault. The web app keeps the draft.
const readOnlyMessage = "the server is read-only after a storage error; reading works, changes wait until " +
	"an administrator fixes it and restarts the server"

// writeReadOnly answers a change to a read-only vault: 503 with the code "read-only" (the web
// app's follow-up #186 reads it) and no Retry-After, since it lasts until a restart
// (ADR-0037). The client keeps its draft.
func writeReadOnly(w stdhttp.ResponseWriter) {
	writeJSON(w, stdhttp.StatusServiceUnavailable, map[string]string{"code": "read-only", "error": readOnlyMessage})
}

// readyz answers 200: reads still work while the vault is read-only, so an orchestrator must
// not restart the server in a loop or take it out of service. The body is a state word only,
// "read-only", never a path or error text (CLAUDE.md rule 15); empty otherwise.
func readyz(v ReadOnly) stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !v.ReadOnly() {
			w.WriteHeader(stdhttp.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(stdhttp.StatusOK)
		_, _ = w.Write([]byte("read-only\n"))
	}
}
