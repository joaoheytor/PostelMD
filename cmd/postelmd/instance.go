package main

import (
	"context"
	"log/slog"
	"os"

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

// buildFormat is the data format this build writes, layout.FormatVersion. Tests set it to 2
// to run format 2's startup and commands (ADR-0042) before FormatVersion is raised.
var buildFormat = layout.FormatVersion

// dataStart is what the format check found (ADR-0030's step 2).
type dataStart struct {
	format  int  // the data's format; a missing file is 1 from build format 2 (ADR-0042)
	upgrade bool // build format 2: the first upgrade runs (format 1) or resumes (its intent is there)
}

// geteuid is os.Geteuid; tests replace it to be another user.
var geteuid = os.Geteuid

// checkOwner is the owner check of every command that changes the state with the server
// stopped, from build format 2 (ADR-0042); at format 1 it checks nothing.
func checkOwner(state string) error {
	if buildFormat < 2 {
		return nil
	}
	return layout.CheckOwner(state, geteuid())
}

// openInstance runs the startup steps after the listener (ADR-0030's order, as far as this
// build goes): the data format (ADR-0025), app.db with its check, migrations and this start's
// boot UUID, the auth state, go-git's transports, from build format 2 the format table's
// vault check, the vault (adoption check, temp sweep, rename probe) and its git repository,
// from build format 2 the first upgrade, then the startup scan's first pass when the vault
// has file IDs (ADR-0031). Nothing is opened before the format check.
func openInstance(ctx context.Context, paths layout.Paths, boot string, log *slog.Logger) (*instance, error) {
	if err := ctx.Err(); err != nil { // stopped while the listener came up
		return nil, err
	}
	start, err := checkDataFormat(paths.State)
	if err != nil {
		return nil, err
	}
	db, err := openAppDB(ctx, paths.State, boot, start, log)
	if err != nil {
		return nil, err
	}
	in := &instance{db: db}
	if in.auth, err = auth.OpenDir(paths.State, audit.New(log), log); err != nil {
		in.close(log, true)
		return nil, err
	}
	installGitTransports()
	// Before the vault opens: opening adopts the folder, writing a marker the table reads.
	if err := checkVaultAtStart(ctx, db, start, paths.Vault, log); err != nil {
		in.close(log, true)
		return nil, err
	}
	if in.vault, err = openVault(paths.Vault, log); err != nil {
		in.close(log, true)
		return nil, err
	}
	// Before the upgrade and the scan, the first writes after the boot UUID (ADR-0030's I/O
	// errors, ADR-0037).
	db.OnWriterFault(goReadOnly(in.vault, log))
	if err := firstUpgrade(ctx, db, in.vault, start, paths, log); err != nil {
		in.close(log, true)
		return nil, err
	}
	if in.ids, in.stopIDs, err = startIDs(ctx, db, in.vault, paths.Vault, log); err != nil {
		in.close(log, true)
		return nil, err
	}
	return in, nil
}

// goReadOnly is the database's writer fault hook: it puts v read-only and logs it once, as an
// error record without the failed write's text (the writer logs that, pathless; CLAUDE.md
// rule 15).
func goReadOnly(v *vault.Vault, log *slog.Logger) func() {
	return func() {
		if v.SetReadOnly() {
			log.Error("the database couldn't be written, so the vault is read-only: reads work, changes are refused "+
				"until the server restarts (ADR-0030). Check the disk and the state directory, then restart", "event", "read_only")
		}
	}
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
