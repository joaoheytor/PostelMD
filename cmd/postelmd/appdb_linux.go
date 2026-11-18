//go:build linux

package main

import (
	"context"
	"log/slog"

	"github.com/joaoheytor/PostelMD/internal/audit"
	pmhttp "github.com/joaoheytor/PostelMD/internal/http"
	"github.com/joaoheytor/PostelMD/internal/ids"
	"github.com/joaoheytor/PostelMD/internal/intents"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// openAppDB opens (creating if absent) state/app.db, checks and migrates it (ADR-0030), and
// records boot as this start's boot UUID (ADR-0014). The caller holds the instance lock and
// has passed the format check (CLAUDE.md rule 12). Data at format 2 with no upgrade to
// finish must have its app.db already (a lost one is refused, naming db rebuild), and a
// damaged one is refused naming db rebuild (ADR-0034).
func openAppDB(ctx context.Context, state, boot string, start dataStart, log *slog.Logger) (appDB, error) {
	open := sqlstore.Open
	if start.format >= 2 && !start.upgrade {
		open = sqlstore.OpenExisting
	}
	db, err := open(ctx, state, log)
	if err != nil {
		return nil, err
	}
	if err := db.SetBootUUID(ctx, boot); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// recoverIntents runs intent recovery (ADR-0033) after the first upgrade, then the temp
// folder's sweep, which spares the temps of the intents left (the flagged ones), and logs
// what recovery did. The scan runs after both.
func recoverIntents(ctx context.Context, db appDB, v *vault.Vault, state string, log *slog.Logger) error {
	sdb, ok := db.(*sqlstore.DB)
	if !ok {
		return v.Sweep(nil)
	}
	t, err := intents.Recover(ctx, intents.Recovery{DB: sdb, Vault: v, State: state, Log: log, Audit: audit.New(log)})
	if err != nil {
		return err
	}
	log.Info("intent recovery", "rolled_forward", t.RolledForward, "dropped", t.Dropped, "kept", t.Kept, "flagged", t.Flagged)
	spare, err := intents.Remaining(ctx, sdb)
	if err != nil {
		return err
	}
	return v.Sweep(spare)
}

// startIDs runs the startup scan (ADR-0030's step 7, ADR-0031) when the database's file IDs
// describe this vault: the "assigned" flag is set and the vault marker names the database's
// vault UUID. The format table has run, so a marker that doesn't name the database's vault
// is an error (ADR-0036's decision 6, stated in ADR-0040). It returns the reads' hook (nil
// without a scan) and the function that stops the background passes; err is ctx's if ctx
// ended during the scan.
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
	if err == nil && m.VaultUUID != id.VaultUUID {
		err = ids.ErrAnotherVault
	}
	if err != nil {
		return nil, nop, err
	}
	s := ids.New(sdb, v, log)
	stop, err := s.Start(ctx)
	if err != nil {
		return nil, nop, err
	}
	return s, stop, nil
}
