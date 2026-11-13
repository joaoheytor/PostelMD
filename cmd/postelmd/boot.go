package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/joaoheytor/PostelMD/internal/errattr"
)

// appDB is what serve needs from the identity database, /data/.postelmd/app.db (ADR-0030).
type appDB interface {
	BootUUID(ctx context.Context) (string, error)
	Close() error
}

// defaultBootCheckEvery is how often a running server reads the boot UUID back (ADR-0014).
const defaultBootCheckEvery = 10 * time.Second

// bootCheckEvery is defaultBootCheckEvery; tests shorten it.
var bootCheckEvery = defaultBootCheckEvery

// errSplitBrain: the boot UUID in app.db is no longer this process's.
var errSplitBrain = errors.New("another PostelMD process started on this data directory " +
	"(the database records another boot UUID), so this one stopped serving and writing (ADR-0014). " +
	"Make sure only one instance mounts the volume (for a container, check `docker ps`; in " +
	"Kubernetes, that the old pod is gone), then start one again")

// watchBoot reads the boot UUID from db every bootCheckEvery until ctx ends (nil), or until
// it isn't want (errSplitBrain): a second writer slipped past the instance lock, for example
// on a filesystem where flock is emulated. A failed read is logged and retried.
func watchBoot(ctx context.Context, db appDB, want string, log *slog.Logger) error {
	t := time.NewTicker(bootCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		got, err := db.BootUUID(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("couldn't read the boot UUID back from the database", errattr.Pathless(err))
		case err == nil && got != want:
			// Logged as an error record; the split-brain metric arrives with /metrics (v0.3).
			log.Error("split brain: another process recorded its start in the database; stopping",
				"event", "split_brain")
			return errSplitBrain
		}
	}
}
