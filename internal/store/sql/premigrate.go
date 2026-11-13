//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Migrating an existing database (user_version ≥ 1) follows ADR-0015's "Snapshot crash
// safety": migrate.intent is written and fsynced first; the snapshot goes to
// pre-migrate-<from>-<to>.db.tmp, is fsynced and verified (integrity_check, user_version =
// from), renamed into place and the folder fsynced; then each migration runs in its own
// transaction; then the post_migration_started marker is written (meta row and sidecar file,
// both durable) and the intent removed. With an intent found at open, the runner reads the
// intent, user_version, the schema_migrations records (verifyApplied, before) and the
// snapshot together, and acts on ADR-0015's table:
//
//	| user_version       | Snapshot                    | Action                                     |
//	|--------------------|-----------------------------|--------------------------------------------|
//	| from               | absent (its temp at most)   | remove the temp, take the snapshot again,  |
//	|                    |                             | migrate                                    |
//	| from               | present, verifies, no temp  | migrate                                    |
//	| from < v < to      | present, verifies, no temp  | resume the remaining migrations            |
//	| to                 | present, no temp            | write the markers if missing, remove the   |
//	|                    |                             | intent                                     |
//	| anything else (or the intent's `to` isn't this build's last migration, or the     |
//	| snapshot fails verification or isn't a regular file) | refuse, change nothing    |
//
// Nothing is ever inferred from one signal: a version alone doesn't say the snapshot exists,
// and a snapshot file alone doesn't say it is whole.

// migrateExisting starts a migration of an existing database at version from to the last of
// ms. A snapshot (or its temp) already there without an intent isn't this run's: refused.
func migrateExisting(ctx context.Context, db *DB, ms []migration, from int) (migrated, error) {
	to := len(ms)
	in := intent{From: from, To: to, Snapshot: snapshotName(from, to)}
	snap := filepath.Join(filepath.Dir(db.path), in.Snapshot)
	for _, p := range []string{snap, snap + ".tmp"} {
		ok, err := present(p)
		if err == nil && ok {
			err = fmt.Errorf("%w (a pre-migration snapshot is already there without an intent)", ErrMigrateInterference)
		}
		if err != nil {
			return migrated{from: from, to: from}, err
		}
	}
	if err := writeIntent(filepath.Dir(db.path), in); err != nil {
		return migrated{from: from, to: from}, err
	}
	return resume(ctx, db, ms, from, in)
}

// resume takes the database at version uv from in.From to in.To by the table above.
func resume(ctx context.Context, db *DB, ms []migration, uv int, in intent) (migrated, error) {
	start := time.Now()
	done := migrated{from: uv, to: uv, snapshot: in.Snapshot}
	refuse := func(why string) (migrated, error) {
		return done, fmt.Errorf("%w (%s: intent %d → %d, database version %d, this build's %d)",
			ErrMigrateInterference, why, in.From, in.To, uv, len(ms))
	}
	if in.To != len(ms) {
		return refuse("the intent was written by another build")
	}
	snap := filepath.Join(filepath.Dir(db.path), in.Snapshot)
	have, err := present(snap)
	if err != nil {
		return done, err
	}
	tmp, err := present(snap + ".tmp")
	if err != nil {
		return done, err
	}
	switch {
	case uv == in.From && !have:
		if err := takeSnapshot(ctx, db, snap, in.From); err != nil {
			return done, err
		}
	case tmp:
		return refuse("a snapshot temp beside the snapshot")
	case !have || uv < in.From || uv > in.To:
		return refuse("the version and the snapshot don't match a step of the intent")
	case uv < in.To:
		if err := verifySnapshot(ctx, snap, in.From); err != nil {
			return done, err
		}
	}
	for _, m := range ms[uv:in.To] {
		if err := db.writeTx(ctx, func(tx *sql.Tx) error {
			if err := apply(ctx, tx, m); err != nil {
				return err
			}
			killPoint("before-commit")
			return nil
		}); err != nil {
			return done, err
		}
		killPoint("after-commit")
		done.to = m.version
	}
	if err := markStarted(ctx, db, in); err != nil {
		return done, err
	}
	if err := removeIntent(filepath.Dir(db.path)); err != nil {
		return done, err
	}
	done.to, done.took = in.To, time.Since(start)
	return done, nil
}

// takeSnapshot removes a partial temp, then copies the database to snap's temp (Snapshot:
// created 0600, fsynced, integrity_check), checks its version is from, renames it into place
// and fsyncs the folder. Migrations start only after that (ADR-0015).
func takeSnapshot(ctx context.Context, db *DB, snap string, from int) error {
	tmp := snap + ".tmp"
	for _, p := range []string{tmp, tmp + "-journal"} {
		if err := removeRegular(p); err != nil {
			return err
		}
	}
	v, err := db.Snapshot(ctx, tmp)
	if err != nil {
		return err
	}
	if v != from {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w (the snapshot has version %d, not %d)", ErrMigrateInterference, v, from)
	}
	killPoint("snapshot-verified")
	// Under the instance lock nothing else writes the state directory, and present(snap)
	// was false just before: the rename replaces nothing.
	if err := os.Rename(tmp, snap); err != nil {
		return fmt.Errorf("sql: put the snapshot in place: %w", err)
	}
	killPoint("snapshot-renamed")
	if err := syncDir(snap); err != nil {
		return fmt.Errorf("sql: put the snapshot in place: %w", err)
	}
	killPoint("snapshot-synced")
	return nil
}

// verifySnapshot checks a snapshot already in place again: integrity_check and version from.
func verifySnapshot(ctx context.Context, snap string, from int) error {
	v, err := checkCopy(ctx, snap)
	if err != nil {
		return errors.Join(ErrMigrateInterference, err)
	}
	if v != from {
		return fmt.Errorf("%w (the snapshot has version %d, not %d)", ErrMigrateInterference, v, from)
	}
	return nil
}

// markStarted closes database-only rollback for this migration (ADR-0015) before Open
// returns the database to a caller that may change it: the meta row, then the sidecar file,
// each durable, each written only if missing.
func markStarted(ctx context.Context, db *DB, in intent) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING", markerKey(in.From, in.To), now)
		return err
	}); err != nil {
		return fmt.Errorf("sql: write the post_migration_started row: %w", err)
	}
	killPoint("marked-db")
	path := filepath.Join(filepath.Dir(db.path), markerName(in.From, in.To))
	f, err := createExclusive(path) // empty, fsynced, folder fsynced
	if errors.Is(err, os.ErrExist) {
		if ok, perr := present(path); perr != nil || !ok {
			return errors.Join(fmt.Errorf("%w (the post_migration_started file)", ErrMigrateInterference), perr)
		}
		err = nil
	} else if err == nil {
		err = f.Close()
	}
	if err != nil {
		return fmt.Errorf("sql: write the post_migration_started file: %w", err)
	}
	killPoint("marked")
	return nil
}
