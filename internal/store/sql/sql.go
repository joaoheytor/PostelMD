//go:build linux

// Package sql is the only package that touches SQLite (CLAUDE.md rule 1; ADR-0030). It opens
// /data/.postelmd/app.db with ADR-0030's pragmas and connections (one writer, a read pool, a
// snapshot connection opened per copy), creates every database file 0600 with O_EXCL before
// SQLite opens it, checks it with quick_check, and runs the embedded, forward-only migrations.
// Every connection denies ATTACH and DETACH.
//
// Linux only, like the server: the other build targets don't compile it.
//
// Callers hold the instance lock and have passed the data format check (CLAUDE.md rule 12).
package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/joaoheytor/PostelMD/internal/errattr"
)

// AppDB is the identity database's file name in the state directory (ADR-0030).
const AppDB = "app.db"

// writerWait bounds the wait for the writer connection (ADR-0030: never unbounded).
var writerWait = 5 * time.Second

var (
	// ErrReplaced: the database file changed identity while it was being opened.
	ErrReplaced = errors.New("sql: the database file was replaced while it was being opened")
	// ErrCheckpointBusy: the WAL couldn't be checkpointed at close (a reader held it).
	ErrCheckpointBusy = errors.New("sql: the WAL checkpoint at close was blocked by a reader")
)

// DB is an open database: the writer and the read pool.
type DB struct {
	path string
	w    *sql.DB // one connection, BEGIN IMMEDIATE
	r    *sql.DB // mode=ro, query_only
}

// Open opens (creating if absent) stateDir/app.db, checks it with quick_check and migrates
// it. log gets the warnings about group or world permission bits.
func Open(ctx context.Context, stateDir string, log *slog.Logger) (*DB, error) {
	return open(ctx, stateDir, log, migrationFiles)
}

// open is Open with the migrations in fsys (tests use their own).
func open(ctx context.Context, stateDir string, log *slog.Logger, fsys fs.FS) (*DB, error) {
	if err := checkAbs(stateDir); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, AppDB)
	f, err := createExclusive(path, "database file")
	if err == nil {
		err = f.Close()
	}
	if err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	killPoint("created")
	before, err := checkRegular(path)
	if err != nil {
		return nil, err
	}
	if err := fixSidecars(path); err != nil {
		return nil, err
	}
	warnLooseModes(log, stateDir, path)
	if err := checkForeign(ctx, path); err != nil {
		log.Log(ctx, failLevel(err), "database refused before opening", "reason", refusal(err), "damaged", errors.Is(err, ErrDamaged),
			errattr.Pathless(err))
		return nil, err
	}
	killPoint("checked")
	db := &DB{path: path}
	if db.w, err = openPool(path, writerQuery, 1); err != nil {
		return nil, err
	}
	if after, err := os.Stat(path); err != nil || !os.SameFile(before, after) {
		_ = db.w.Close()
		return nil, errors.Join(ErrReplaced, err)
	}
	killPoint("opened")
	if err := db.start(ctx, log, fsys); err != nil {
		_ = db.w.Close()
		return nil, err
	}
	// The read pool opens after the writer has put the file in WAL mode: a mode=ro
	// connection can't change the journal mode.
	if db.r, err = openPool(path, readerQuery, readPoolSize); err != nil {
		_ = db.w.Close()
		return nil, err
	}
	return db, nil
}

// start runs quick_check, then the migrations in fsys, on the writer, and logs both
// (ADR-0030's structured records).
func (db *DB) start(ctx context.Context, log *slog.Logger, fsys fs.FS) error {
	t := time.Now()
	if err := quickCheck(ctx, db.w); err != nil {
		log.Log(ctx, failLevel(err), "database check failed", "check", "quick_check", "damaged", errors.Is(err, ErrDamaged),
			"duration_ms", time.Since(t).Milliseconds(), errattr.Pathless(err))
		return err
	}
	log.Info("database checked", "check", "quick_check", "result", "ok", "duration_ms", time.Since(t).Milliseconds())
	m, err := migrate(ctx, db, fsys)
	if err != nil {
		log.Log(ctx, failLevel(err), "database migration refused or failed", "from", m.from, "reason", refusal(err), errattr.Pathless(err))
		return err
	}
	if m.to != m.from || m.snapshot != "" {
		// The snapshot is named by its file name in the state directory, never by its path.
		log.Info("database migrated", "from", m.from, "to", m.to, "duration_ms", m.took.Milliseconds(), "snapshot", m.snapshot)
	}
	return nil
}

// failLevel is the level of a failed open step's record: INFO when a clean stop canceled
// it (the step stopped, nothing is wrong), ERROR otherwise.
func failLevel(err error) slog.Level {
	if errors.Is(err, context.Canceled) {
		return slog.LevelInfo
	}
	return slog.LevelError
}

// Close closes the read pool, checkpoints the WAL into the database (TRUNCATE, ADR-0030's
// clean stop; a checkpoint a reader blocked is ErrCheckpointBusy) and closes the writer.
func (db *DB) Close() error {
	rerr := db.r.Close()
	var busy, logFrames, done int
	cerr := db.w.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &done)
	if cerr == nil && busy != 0 {
		cerr = ErrCheckpointBusy
	}
	return errors.Join(rerr, cerr, db.w.Close())
}

// writeTx runs fn in one write transaction (BEGIN IMMEDIATE). The writer connection is taken
// within writerWait; fn must not use the writer pool itself (one connection: it would
// deadlock, ADR-0030).
func (db *DB) writeTx(ctx context.Context, fn func(*sql.Tx) error) error {
	wait, cancel := context.WithTimeout(ctx, writerWait)
	conn, err := db.w.Conn(wait)
	cancel()
	if err != nil {
		return fmt.Errorf("sql: the writer is busy: %w", err)
	}
	defer func() { _ = conn.Close() }()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sql: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sql: commit: %w", err)
	}
	return nil
}
