package main

import (
	"context"
	"log/slog"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/auth"
	"github.com/joaoheytor/PostelMD/internal/errattr"
	pmhttp "github.com/joaoheytor/PostelMD/internal/http"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// instance is what serve opens behind the listener: the database, the auth state, the
// vault, and the startup scan's read hook (nil until file IDs are assigned) with the
// function that stops its background work.
type instance struct {
	db      appDB
	auth    *auth.Service
	vault   *vault.Vault
	ids     pmhttp.IDs
	stopIDs func()
}

// openInstance runs the startup steps after the listener (ADR-0030's order, as far as this
// build goes): the data format (ADR-0025), app.db with its check, migrations and this start's
// boot UUID, the auth state, go-git's transports, the vault (adoption check, temp sweep,
// rename probe) and its git repository, then the startup scan's first pass when the vault
// has file IDs (ADR-0031). Nothing is opened before the format check.
func openInstance(ctx context.Context, paths layout.Paths, boot string, log *slog.Logger) (*instance, error) {
	if err := ctx.Err(); err != nil { // stopped while the listener came up
		return nil, err
	}
	if err := layout.EnsureFormat(paths.State); err != nil {
		return nil, err
	}
	db, err := openAppDB(ctx, paths.State, boot, log)
	if err != nil {
		return nil, err
	}
	in := &instance{db: db}
	if in.auth, err = auth.OpenDir(paths.State, audit.New(log)); err != nil {
		in.close(log, true)
		return nil, err
	}
	installGitTransports()
	if in.vault, err = openVault(paths.Vault, log); err != nil {
		in.close(log, true)
		return nil, err
	}
	if in.ids, in.stopIDs, err = startIDs(ctx, db, in.vault, paths.Vault, log); err != nil {
		in.close(log, true)
		return nil, err
	}
	return in, nil
}

// close stops the scan's background work, then closes what is open. With checkpoint false
// (a split brain: another process may be writing the same database), the database is left
// as it is, unclosed: closing the last connection would checkpoint the WAL into it.
func (in *instance) close(log *slog.Logger, checkpoint bool) {
	if in.stopIDs != nil {
		in.stopIDs()
	}
	if in.vault != nil {
		_ = in.vault.Close()
	}
	if in.auth != nil {
		_ = in.auth.Close()
	}
	if !checkpoint {
		return
	}
	if err := in.db.Close(); err != nil {
		// The WAL replays at the next open; a failed checkpoint loses nothing.
		log.Warn("the database didn't close cleanly", errattr.Pathless(err))
	}
}
