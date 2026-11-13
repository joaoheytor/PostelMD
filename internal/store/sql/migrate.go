//go:build linux

package sql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

//go:embed migrations/*.sql
var embedded embed.FS

// migrationFiles are this build's migrations, migrations/NNNN_slug.sql (ADR-0030).
var migrationFiles = func() fs.FS {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		panic(err) // the embedded tree always has the folder
	}
	return sub
}()

var (
	// ErrNewerDatabase: user_version is above this build's last migration. Nothing changed.
	ErrNewerDatabase = errors.New("sql: this database was written by a newer PostelMD; " +
		"run that version, or restore a backup taken before the upgrade (ADR-0015)")
	// ErrEditedMigration: an applied migration's recorded checksum differs from the embedded
	// one. Released migrations are never edited.
	ErrEditedMigration = errors.New("sql: an applied migration differs from this build's; " +
		"this build can't use the database (ADR-0030)")
	// ErrForeignDatabase: user_version is 0 but the file has tables. Nothing changed.
	ErrForeignDatabase = errors.New("sql: app.db has tables but no PostelMD version, so another " +
		"program made it; move it out of the state directory and start again (ADR-0030)")
)

// refusal names which refusal err is, for the log: a fixed word, never err's text.
func refusal(err error) string {
	for _, r := range []struct {
		err  error
		name string
	}{{ErrNewerDatabase, "newer-database"}, {ErrEditedMigration, "edited-migration"}, {ErrMigrateInterference, "migrate-interference"},
		{ErrForeignDatabase, "foreign-database"}} {
		if errors.Is(err, r.err) {
			return r.name
		}
	}
	return "error"
}

// killPoint is called at each crash point of Open and the migration runner. Production
// leaves it a no-op; the crash tests kill the process there (CLAUDE.md rule 14), and the
// race tests swap files there.
var killPoint = func(string) {}

// checkForeign refuses a database at user_version 0 that already has a schema: another
// program's file, not one PostelMD created and never migrated. It reads the file on a
// read-only connection without the WAL pragma, before the writer opens it, so a refused
// file is left exactly as it was (the writer would switch it to WAL).
func checkForeign(ctx context.Context, path string) error {
	p, err := openPool(path, copyQuery, 1)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()
	var uv, n int
	if err := p.QueryRowContext(ctx, "PRAGMA user_version").Scan(&uv); err != nil {
		return damaged(fmt.Errorf("sql: read user_version: %w", err))
	}
	if err := p.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&n); err != nil {
		return damaged(fmt.Errorf("sql: read the schema: %w", err))
	}
	if uv == 0 && n != 0 {
		return ErrForeignDatabase
	}
	return nil
}

type migration struct {
	version int
	name    string
	sql     string
	sum     string // hex SHA-256 of the file
}

var migrationName = regexp.MustCompile(`^([0-9]{4})_([a-z0-9_]+)\.sql$`)

// loadMigrations reads fsys's *.sql files: well-named, numbered from 1 without gaps.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	names, err := fs.Glob(fsys, "*.sql") // sorted
	if err != nil {
		return nil, err
	}
	ms := make([]migration, 0, len(names))
	for i, n := range names {
		m := migrationName.FindStringSubmatch(n)
		if m == nil {
			return nil, fmt.Errorf("sql: migration %q isn't named NNNN_slug.sql", n)
		}
		v, _ := strconv.Atoi(m[1])
		if v != i+1 {
			return nil, fmt.Errorf("sql: migration %q: expected number %04d (no gaps, from 1)", n, i+1)
		}
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		ms = append(ms, migration{version: v, name: n, sql: string(b), sum: hex.EncodeToString(sum[:])})
	}
	return ms, nil
}

// migrated describes what migrate did, for the log record.
type migrated struct {
	from, to int
	took     time.Duration
	snapshot string // the pre-migration snapshot's name in the state directory, if one was taken
}

// migrate brings the database to fsys's last migration. A new database (user_version 0 and
// an empty sqlite_schema) gets every migration in one transaction, each setting user_version
// and recording its row, so a crash leaves version 0 and the next start runs them again
// (ADR-0030). An existing database with pending migrations, or an open migrate.intent, goes
// through the pre-migration snapshot (premigrate.go). A newer database, an edited migration
// or a foreign database (version 0 with tables) is refused, changing nothing.
func migrate(ctx context.Context, db *DB, fsys fs.FS) (migrated, error) {
	start := time.Now()
	ms, err := loadMigrations(fsys)
	if err != nil {
		return migrated{}, err
	}
	var uv int
	if err := db.w.QueryRowContext(ctx, "PRAGMA user_version").Scan(&uv); err != nil {
		return migrated{}, fmt.Errorf("sql: read user_version: %w", err)
	}
	done := migrated{from: uv, to: uv}
	if uv > len(ms) {
		return done, fmt.Errorf("%w (database version %d, this build's %d)", ErrNewerDatabase, uv, len(ms))
	}
	if uv > 0 {
		if err := verifyApplied(ctx, db.w, ms[:uv]); err != nil {
			return done, err
		}
	}
	in, err := readIntent(filepath.Dir(db.path))
	switch {
	case err != nil:
		return done, err
	case in != nil:
		return resume(ctx, db, ms, uv, *in)
	case uv == len(ms):
		return done, nil
	case uv > 0:
		return migrateExisting(ctx, db, ms, uv)
	}
	err = db.writeTx(ctx, func(tx *sql.Tx) error {
		for _, m := range ms {
			if err := apply(ctx, tx, m); err != nil {
				return err
			}
		}
		killPoint("before-commit")
		return nil
	})
	if err != nil {
		return done, err
	}
	killPoint("after-commit")
	done.to, done.took = len(ms), time.Since(start)
	return done, nil
}

// apply runs one migration inside tx, sets user_version and records its row.
func apply(ctx context.Context, tx *sql.Tx, m migration) error {
	killPoint("before-sql")
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("sql: migration %s: %w", m.name, err)
	}
	killPoint("before-version")
	// PRAGMA takes no parameters; m.version is an int from a checked file name.
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(m.version)); err != nil {
		return fmt.Errorf("sql: migration %s: set user_version: %w", m.name, err)
	}
	killPoint("before-record")
	_, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, name, sha256, applied_at) VALUES (?, ?, ?, ?)",
		m.version, m.name, m.sum, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("sql: migration %s: record: %w", m.name, err)
	}
	return nil
}

// verifyApplied checks that schema_migrations holds exactly ms, with their checksums.
func verifyApplied(ctx context.Context, p *sql.DB, ms []migration) error {
	rows, err := p.QueryContext(ctx, "SELECT version, name, sha256 FROM schema_migrations ORDER BY version")
	if err != nil {
		return errors.Join(ErrEditedMigration, err)
	}
	defer func() { _ = rows.Close() }()
	i := 0
	for ; rows.Next(); i++ {
		var v int
		var name, sum string
		if err := rows.Scan(&v, &name, &sum); err != nil {
			return errors.Join(ErrEditedMigration, err)
		}
		if i >= len(ms) || v != ms[i].version || name != ms[i].name || sum != ms[i].sum {
			return fmt.Errorf("%w (migration %d)", ErrEditedMigration, v)
		}
	}
	if err := rows.Err(); err != nil {
		return errors.Join(ErrEditedMigration, err)
	}
	if i != len(ms) {
		return fmt.Errorf("%w (%d recorded, user_version %d)", ErrEditedMigration, i, len(ms))
	}
	return nil
}
