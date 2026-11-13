//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"

	// The driver needs modernc.org/libc at exactly the version its go.mod names. Imported
	// here (it is linked anyway) so our go.mod requires it directly, and `go mod tidy` or a
	// bump of another module can't move it on its own (ADR-0015, clarification of
	// 2026-11-12; scripts/check-sqlite-libc.sh checks it).
	_ "modernc.org/libc"
	sqlite "modernc.org/sqlite"
)

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

// SQLite's primary result codes that mean the file itself is damaged or isn't a database.
const (
	codeCorrupt = 11 // SQLITE_CORRUPT
	codeNotADB  = 26 // SQLITE_NOTADB
)

// ErrDamaged: the file isn't a database, or quick_check found a problem. Startup refuses and
// changes nothing. Only those causes give it: a canceled context, a busy lock, an I/O or
// permission error are plain errors. Until `postelmd db rebuild` arrives with data format 2,
// the fix is to move the files aside: at format 1 the database holds only the boot UUID, and
// the next start creates a new one (ADR-0034).
var ErrDamaged = errors.New("sql: the database is damaged or isn't a database. With the server " +
	"stopped, move app.db, app.db-wal and app.db-shm together into a new folder such as " +
	".postelmd/damaged-<date>/, and never delete them; then start again. At this data format " +
	"the database holds only the boot UUID, so nothing is lost (ADR-0030, ADR-0034)")

// damaged wraps err with ErrDamaged when SQLite says the file is corrupt or not a database.
func damaged(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) {
		if c := se.Code() & 0xff; c == codeCorrupt || c == codeNotADB {
			return errors.Join(ErrDamaged, err)
		}
	}
	return err
}

// installPolicy makes every connection of the driver, in this process, deny ATTACH and
// DETACH: nothing needs them, and through ATTACH a read-only connection could open app.db
// (or a new file) read-write. VACUUM INTO is an ATTACH to the authorizer too, so Snapshot's
// own connection replaces this policy with one that allows an ATTACH of exactly its target
// (vacuumInto). It also turns on the driver's StrictPragmas (process-wide): a _pragma
// holding two statements fails to open.
//
// Every sql.Open("sqlite") in the repository goes through openPool, so this hook is always
// installed first (TestOnlyOpenPoolOpensSQLite checks it).
var installPolicy = sync.OnceFunc(func() {
	sqlite.StrictPragmas(true)
	sqlite.RegisterConnectionHook(func(c sqlite.ExecQuerierContext, _ string) error {
		r, ok := c.(sqlite.AuthorizerRegisterer)
		if !ok {
			return errors.New("sql: the driver has no authorizer")
		}
		return r.RegisterAuthorizer(denyAttach)
	})
})

func denyAttach(action sqlite.AuthorizerActionCode, _, _, _, _ string) sqlite.AuthorizerReturnCode {
	if action == sqlite.AuthAttach || action == sqlite.AuthDetach {
		return sqlite.AuthorizerDeny
	}
	return sqlite.AuthorizerOK
}

// ErrRelativePath: a database path isn't absolute. A relative one would be
// misread: in a file: URI, "localhost/x" becomes an authority (SQLite opens /x while Go
// checked ./localhost/x), and "file:x.db" as a VACUUM INTO target is a URI of its own.
var ErrRelativePath = errors.New("sql: database paths must be absolute")

// The paths openPool gets are clean: filepath.Join's, or a dest Snapshot cleaned.

// checkAbs refuses a path that isn't absolute; it never resolves one against the working
// directory. Callers clean what they accept.
func checkAbs(path string) error {
	if !filepath.IsAbs(path) {
		return ErrRelativePath
	}
	return nil
}

// fileURI is path, absolute (checkAbs), as a file: URI with query q.
func fileURI(path, q string) string {
	u := url.URL{Scheme: "file", Path: path, RawQuery: q}
	return u.String()
}

// openPool opens a database/sql pool of at most n connections on path, and opens its first
// connection so a bad DSN or file fails here (ErrDamaged if it isn't a database).
func openPool(path, q string, n int) (*sql.DB, error) {
	if err := checkAbs(path); err != nil {
		return nil, err
	}
	installPolicy()
	p, err := sql.Open("sqlite", fileURI(path, q))
	if err != nil {
		return nil, fmt.Errorf("sql: open: %w", err)
	}
	p.SetMaxOpenConns(n)
	p.SetMaxIdleConns(n)
	p.SetConnMaxLifetime(0)
	if err := p.Ping(); err != nil {
		_ = p.Close()
		return nil, damaged(fmt.Errorf("sql: open: %w", err))
	}
	return p, nil
}

// quickCheck runs PRAGMA quick_check(1): nil for "ok", ErrDamaged for a finding or a
// corrupt file, a plain error otherwise.
func quickCheck(ctx context.Context, p *sql.DB) error {
	var res string
	if err := p.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&res); err != nil {
		return damaged(fmt.Errorf("sql: quick_check: %w", err))
	}
	if res != "ok" {
		return ErrDamaged
	}
	return nil
}
