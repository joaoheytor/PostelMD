//go:build linux

package main

import (
	"context"
	"log/slog"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// openAppDB opens (creating if absent) state/app.db, checks and migrates it (ADR-0030), and
// records boot as this start's boot UUID (ADR-0014). The caller holds the instance lock and
// has passed the format check (CLAUDE.md rule 12).
func openAppDB(ctx context.Context, state, boot string, log *slog.Logger) (appDB, error) {
	db, err := sqlstore.Open(ctx, state, log)
	if err != nil {
		return nil, err
	}
	if err := db.SetBootUUID(ctx, boot); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
