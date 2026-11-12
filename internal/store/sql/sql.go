//go:build linux

// Package sql is the only package that touches SQLite (CLAUDE.md rule 1; ADR-0030). It opens
// /data/.postelmd/app.db with ADR-0030's pragmas and connections (one writer, a read pool, a
// snapshot connection opened per copy), creates every database file 0600 with O_EXCL before
// SQLite opens it, checks it with quick_check, and runs the embedded, forward-only migrations.
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
	"net/url"
	"os"
	"path/filepath"
	"time"

	// The driver needs modernc.org/libc at exactly the version its go.mod names. Imported
	// here (it is linked anyway) so our go.mod requires it directly, and `go mod tidy` or a
	// bump of another module can't move it on its own (ADR-0015, clarification of
	// 2026-11-12).
	_ "modernc.org/libc"
	sqlite "modernc.org/sqlite"

	"github.com/joaoheytor/PostelMD/internal/errattr"
)

// AppDB is the identity database's file name in the state directory (ADR-0030).
const AppDB = "app.db"

// The DSN query parts are compile-time constants (ADR-0030); only the path is added at run
// time, escaped into a file: URI (with a plain name the driver strips the query, so mode=ro
// would silently open read-write). Every connection gets the same pragmas.
const (
	pragmas = "_defensive=1" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=cell_size_check(ON)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=journal_size_limit(67108864)" +
		"&_pragma=synchronous(FULL)" +
		"&_pragma=trusted_schema(OFF)"
	writerQuery   = pragmas + "&_txlock=immediate"
	readerQuery   = pragmas + "&mode=ro&_pragma=query_only(1)"
	snapshotQuery = pragmas + "&mode=ro" // VACUUM INTO fails under query_only
	readPoolSize  = 4
)

// writerWait bounds the wait for the writer connection (ADR-0030: never unbounded).
var writerWait = 5 * time.Second

// ErrDamaged: quick_check found a problem. Startup refuses and changes nothing; the fix is
// `postelmd db rebuild` (ADR-0030).
var ErrDamaged = errors.New("sql: the database failed its integrity check; " +
	"run `postelmd db rebuild` with the server stopped (ADR-0030)")

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
	sqlite.StrictPragmas(true) // process-wide: a _pragma holding two statements fails to open
	path := filepath.Join(stateDir, AppDB)
	if err := createExclusive(path); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	killPoint("created")
	if err := checkRegular(path); err != nil {
		return nil, err
	}
	warnLooseModes(log, stateDir, path)
	db := &DB{path: path}
	var err error
	if db.w, err = openPool(path, writerQuery, 1); err != nil {
		return nil, err
	}
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
		log.Error("database check failed", "check", "quick_check", "duration_ms", time.Since(t).Milliseconds(),
			errattr.Pathless(err))
		return err
	}
	log.Info("database checked", "check", "quick_check", "result", "ok", "duration_ms", time.Since(t).Milliseconds())
	m, err := migrate(ctx, db, fsys)
	if err != nil {
		log.Error("database migration refused or failed", "from", m.from, "reason", refusal(err), errattr.Pathless(err))
		return err
	}
	if m.to != m.from {
		log.Info("database migrated", "from", m.from, "to", m.to, "duration_ms", m.took.Milliseconds())
	}
	return nil
}

// Close checkpoints the WAL into the database (TRUNCATE, ADR-0030's clean stop) and closes
// every connection.
func (db *DB) Close() error {
	_, cerr := db.w.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return errors.Join(cerr, db.r.Close(), db.w.Close())
}

// fileURI is path as a file: URI with query q.
func fileURI(path, q string) string {
	u := url.URL{Scheme: "file", Path: path, RawQuery: q}
	return u.String()
}

// openPool opens a database/sql pool of at most n connections on path, and opens its first
// connection so a bad DSN or file fails here.
func openPool(path, q string, n int) (*sql.DB, error) {
	p, err := sql.Open("sqlite", fileURI(path, q))
	if err != nil {
		return nil, fmt.Errorf("sql: open: %w", err)
	}
	p.SetMaxOpenConns(n)
	p.SetMaxIdleConns(n)
	p.SetConnMaxLifetime(0)
	if err := p.Ping(); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("sql: open: %w", err)
	}
	return p, nil
}

// quickCheck runs PRAGMA quick_check(1): "ok", or ErrDamaged.
func quickCheck(ctx context.Context, p *sql.DB) error {
	var res string
	if err := p.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&res); err != nil {
		return errors.Join(ErrDamaged, err)
	}
	if res != "ok" {
		return ErrDamaged
	}
	return nil
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
