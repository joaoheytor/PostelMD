//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/config"
	"github.com/joaoheytor/PostelMD/internal/ids"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	"github.com/joaoheytor/PostelMD/internal/store/lock"
)

// offline checks the owner, takes the instance lock and checks the data format, as every
// command that changes the state does with the server stopped (ADR-0025, ADR-0042), then
// runs fn. The lock is released after fn.
func offline(cfg config.Config, fn func(layout.Paths) error) error {
	if err := checkOwner(cfg.DataDir); err != nil {
		return err
	}
	paths, _, err := layout.Prepare(cfg.DataDir, layoutOptions(cfg, true))
	if err != nil {
		return err
	}
	lk, err := lock.Acquire(paths.Lock)
	if err != nil {
		return fmt.Errorf("%w; stop the server first", err)
	}
	defer func() { _ = lk.Release() }()
	if _, err := layout.CheckFormat(paths.State, buildFormat); err != nil {
		return err
	}
	return fn(paths)
}

// dbRebuild makes a new database with new file IDs (ADR-0030, ADR-0040), with the server
// stopped. Its records (a temp-folder entry not copied, the audit record) go to stdout, like serve's.
func dbRebuild(cfg config.Config, stdout io.Writer) error {
	return offline(cfg, func(paths layout.Paths) error {
		ctx := context.Background()
		log := slog.New(slog.NewJSONHandler(stdout, nil))
		r, err := ids.Rebuild(ctx, ids.RebuildOptions{
			State: paths.State, Vault: paths.Vault, Format: buildFormat, Now: time.Now(), Log: log,
		})
		if err != nil {
			return err
		}
		audit.New(log).Record(ctx, "db.rebuilt", slog.String("rebuild_uuid", r.UUID),
			slog.String("vault_uuid", r.VaultUUID), slog.Bool("resumed", r.Resumed), slog.Bool("had_db", r.HadDB),
			slog.Int("files", r.Files), slog.Int("temps_kept", r.Kept), slog.Bool("took_over_upgrade", r.Upgrade))
		_, err = fmt.Fprint(stdout, rebuildMessage(r))
		return err
	})
}

// rebuildMessage says what db rebuild did and what to do next.
func rebuildMessage(r ids.Rebuilt) string {
	var b strings.Builder
	if r.Resumed {
		b.WriteString("Finished the db rebuild that was interrupted. ")
	}
	if r.Files > 0 || !r.Resumed {
		fmt.Fprintf(&b, "Made a new database: %d files have new IDs under a new vault UUID, and %d trash items "+
			"were imported. ", r.Files, r.Trash)
	}
	b.WriteString("The old file IDs are gone.\n")
	if len(r.Moves) > 0 {
		fmt.Fprintf(&b, "The old database's files are in .postelmd/%s/ (never removed automatically).\n",
			r.DamagedDir())
	}
	if r.Upgrade {
		fmt.Fprintf(&b, "It took over a first upgrade that couldn't finish: its upgrade.intent is in .postelmd/%s/, "+
			"and .postelmd/pre-format-2/, if the upgrade made one, stays where it is.\n", r.DamagedDir())
	}
	if r.Kept > 0 {
		fmt.Fprintf(&b, "%d temp files of unfinished saves are in .postelmd/%s/.\n", r.Kept, r.RecoveryDir())
	}
	if r.Skipped > 0 {
		fmt.Fprintf(&b, "%d entries of the vault's temp folder weren't regular files, so they weren't copied; "+
			"the vault's start-up sweep removed them, or removes them at the next start (names in the records above).\n", r.Skipped)
	}
	b.WriteString("Start the server now.\n")
	return b.String()
}
