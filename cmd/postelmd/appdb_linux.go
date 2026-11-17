//go:build linux

package main

import (
	"context"
	"log/slog"

	"github.com/joaoheytor/PostelMD/internal/errattr"
	pmhttp "github.com/joaoheytor/PostelMD/internal/http"
	"github.com/joaoheytor/PostelMD/internal/ids"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
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

// startIDs runs the startup scan (ADR-0030's step 7, ADR-0031) when the database's file IDs
// describe this vault: the "assigned" flag is set and the vault marker names the database's
// vault UUID. Anything else is left as it is, without a scan: at data format 1 nothing
// assigns IDs yet, and the combinations of flag and marker are ADR-0030's format table,
// which arrives with format 2. It returns the reads' hook (nil without a scan) and the
// function that stops the background passes; err is ctx's if ctx ended during the scan.
func startIDs(ctx context.Context, db appDB, v *vault.Vault, vaultDir string, log *slog.Logger) (pmhttp.IDs, func(), error) {
	nop := func() {}
	sdb, ok := db.(*sqlstore.DB)
	if !ok {
		return nil, nop, nil
	}
	id, err := sdb.Identity(ctx)
	if err != nil {
		return nil, nop, err
	}
	if !id.Assigned {
		return nil, nop, nil
	}
	m, err := vault.ReadMarker(vaultDir)
	if err != nil || m.VaultUUID != id.VaultUUID {
		var attrs []any
		if err != nil {
			attrs = append(attrs, errattr.Pathless(err))
		}
		log.Warn("the database has file IDs, but the vault marker doesn't name its vault: the startup scan is skipped "+
			"and nothing changes (data format 2's checks decide; ADR-0030)", attrs...)
		return nil, nop, nil
	}
	s := ids.New(sdb, v, log)
	stop, err := s.Start(ctx)
	if err != nil {
		return nil, nop, err
	}
	return s, stop, nil
}
