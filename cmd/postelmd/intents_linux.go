//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/config"
	"github.com/joaoheytor/PostelMD/internal/intents"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// withIntents runs fn on the database, with the server stopped (the instance lock), the
// same way as vault claim: an upgrade under way is refused, and app.db must exist.
func withIntents(cfg config.Config, fn func(ctx context.Context, paths layout.Paths, db *sqlstore.DB) error) error {
	return offline(cfg, func(paths layout.Paths) error {
		ctx := context.Background()
		if err := sqlstore.UpgradeUnderWay(paths.State); err != nil {
			return err
		}
		db, err := sqlstore.OpenExisting(ctx, paths.State, slog.New(slog.DiscardHandler))
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		return fn(ctx, paths, db)
	})
}

// intentsList prints every intent (ADR-0033): op ID, kind, state, time, and the folder of its
// recovery copies. An open one is the next start's; a flagged one waits for the operator.
func intentsList(cfg config.Config, stdout io.Writer) error {
	return withIntents(cfg, func(ctx context.Context, paths layout.Paths, db *sqlstore.DB) error {
		all, err := intents.List(ctx, db, paths.State)
		if err != nil {
			return err
		}
		if len(all) == 0 {
			_, err := fmt.Fprintln(stdout, "No intents: no operation is unfinished.")
			return err
		}
		for _, l := range all {
			copies := l.Copies
			if copies == "" {
				copies = "-"
			}
			if _, err := fmt.Fprintf(stdout, "%s  %s  %s  %s  %s\n", l.OpID, l.Kind, l.State, l.At.UTC().Format(time.RFC3339), copies); err != nil {
				return err
			}
		}
		return nil
	})
}

// intentsResolve ends the flagged intent cfg.OpID (intents.Resolve). Its audit record goes to
// stdout, the sink serve uses (ADR-0020), as vault claim's.
func intentsResolve(cfg config.Config, stdout io.Writer) error {
	return withIntents(cfg, func(ctx context.Context, paths layout.Paths, db *sqlstore.DB) error {
		v, err := vault.Open(paths.Vault)
		if err != nil {
			return err
		}
		defer func() { _ = v.Close() }()
		log := slog.New(slog.NewJSONHandler(stdout, nil))
		res, err := intents.Resolve(ctx, intents.Recovery{DB: db, Vault: v, State: paths.State, Log: log, Audit: audit.New(log)}, cfg.OpID)
		if err != nil {
			return err
		}
		msg := fmt.Sprintf("Resolved the %s intent %s: the next scan decides its paths again.", res.Kind, cfg.OpID)
		switch {
		case res.TempRemoved:
			msg += " Its temp was removed, after its copy was made."
		case res.TempLeft:
			msg += " Its temp isn't a regular file: the next start removes it."
		}
		if res.Copies != "" {
			msg += " Its copies stay in " + res.Copies + "."
		}
		_, err = fmt.Fprintln(stdout, msg)
		return err
	})
}
