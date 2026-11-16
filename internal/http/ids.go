package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"strconv"
	"time"
)

// IDs is what a read needs from file identity (ADR-0031, internal/ids). Seen gives a path
// the startup scan hasn't seen its ID, or says how long until its pending rename match
// resolves.
type IDs interface {
	Seen(ctx context.Context, p string) (time.Duration, error)
}

// held runs a read's identity pass over p and reports whether it answered: a path held for
// a rename match that resolves in the next seconds gets 503 with Retry-After (the client
// keeps its draft and retries; ADR-0031). An error is logged and the read goes on: no file
// ID is on the wire yet, and the next scan tries again.
func (a *NotesAPI) held(w stdhttp.ResponseWriter, r *stdhttp.Request, p string) bool {
	if a.ids == nil {
		return false
	}
	wait, err := a.ids.Seen(r.Context(), p)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			a.log.Warn("a read couldn't record the file's identity; the next scan does", "route", r.Pattern, pathless(err))
		}
		return false
	}
	if wait <= 0 {
		return false
	}
	w.Header().Set("Retry-After", strconv.FormatInt(int64((wait+time.Second-1)/time.Second), 10))
	writeError(w, stdhttp.StatusServiceUnavailable, "this file was just renamed outside PostelMD; retry in a few seconds")
	return true
}
