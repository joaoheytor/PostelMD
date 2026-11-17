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
// has passed the format check (CLAUDE.md rule 12). From build format 2, data at format 2
// with no upgrade to finish must have its app.db already (a lost one is refused, naming db
// rebuild), and a damaged one is refused naming db rebuild (ADR-0034).
func openAppDB(ctx context.Context, state, boot string, start dataStart, log *slog.Logger) (appDB, error) {
	open := sqlstore.Open
	if buildFormat >= 2 && start.format >= 2 && !start.upgrade {
		open = sqlstore.OpenExisting
	}
	db, err := open(ctx, state, log)
	if err != nil {
		if buildFormat >= 2 {
			return nil, sqlstore.NameRebuild(err)
		}
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
// vault UUID. At build format 1 anything else is left as it is, without a scan and with a
// warning: nothing assigns IDs yet. From build format 2 the format table has run, so a
// marker that doesn't name the database's vault is an error (ADR-0036's decision 6, stated
// in ADR-0040). It returns the reads' hook (nil without a scan) and the function that stops
// the background passes; err is ctx's if ctx ended during the scan.
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
	if buildFormat >= 2 {
		if err == nil && m.VaultUUID != id.VaultUUID {
			err = ids.ErrAnotherVault
		}
		if err != nil {
			return nil, nop, err
		}
	}
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
