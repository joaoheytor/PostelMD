package http

import (
	"log/slog"

	"github.com/joaoheytor/PostelMD/internal/errattr"
)

// pathless describes err for the log without any user text it quotes (CLAUDE.md rule 15).
// Every API logs its unexpected errors with it; the layers below use errattr.Pathless.
func pathless(err error) slog.Attr { return errattr.Pathless(err) }
