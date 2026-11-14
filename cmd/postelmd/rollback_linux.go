//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/joaoheytor/PostelMD/internal/config"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	"github.com/joaoheytor/PostelMD/internal/store/lock"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// dbRollback undoes a database migration that didn't finish (ADR-0035), with the server
// stopped: it takes the instance lock, so it refuses to run next to a live server, and checks
// the data format first, like every command that reads the state (ADR-0025).
func dbRollback(cfg config.Config, stdout io.Writer) error {
	paths, _, err := layout.Prepare(cfg.DataDir, layoutOptions(cfg, true))
	if err != nil {
		return err
	}
	lk, err := lock.Acquire(paths.Lock)
	if err != nil {
		return fmt.Errorf("%w; stop the server first", err)
	}
	defer func() { _ = lk.Release() }()
	if err := layout.EnsureFormat(paths.State); err != nil {
		return err
	}
	r, err := sqlstore.Rollback(context.Background(), paths.State, time.Now())
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(stdout, rollbackMessage(r))
	return err
}

// rollbackMessage says what db rollback did and what to do next.
func rollbackMessage(r sqlstore.RolledBack) string {
	if !r.Restored {
		return fmt.Sprintf("The migration from version %d to %d stopped before it changed the database: "+
			"its intent is removed. Start the previous version now.\n", r.From, r.To)
	}
	return fmt.Sprintf("Rolled back the database from version %d to %d. The database it replaced, its WAL "+
		"files and the snapshot are in .postelmd/%s/. Start the previous version now.\n", r.To, r.From, r.Dir)
}
