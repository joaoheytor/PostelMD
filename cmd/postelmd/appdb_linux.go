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

// readOnlyOnFault makes a writer fault on db (SQLITE_CORRUPT, SQLITE_IOERR or SQLITE_FULL)
// put v read-only (ADR-0030's I/O errors, ADR-0037). Call it before db's first write after
// the boot UUID.
func readOnlyOnFault(db appDB, v *vault.Vault, log *slog.Logger) {
	if sdb, ok := db.(*sqlstore.DB); ok {
		sdb.OnWriterFault(goReadOnly(v, log))
	}
}

// goReadOnly puts v read-only and logs it once, as an error record without the failed
// write's text (the writer logs that, pathless; CLAUDE.md rule 15).
func goReadOnly(v *vault.Vault, log *slog.Logger) func() {
	return func() {
		if v.SetReadOnly() {
			log.Error("the database couldn't be written, so the vault is read-only: reads work, changes are refused "+
				"until the server restarts (ADR-0030). Check the disk and the state directory, then restart", "event", "read_only")
		}
	}
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
