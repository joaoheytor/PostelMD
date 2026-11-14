//go:build linux

package sql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// postelmd db rollback (ADR-0035, superseding part of ADR-0015's database-only rollback):
// with the server stopped, it undoes a migration that never finished, so the previous build
// starts again. It refuses, changing nothing, unless migrate.intent names this build's last
// migration, neither post_migration_started marker exists, app.db is at a version of the
// intent, and the snapshot verifies (integrity_check, version from). Then it writes
// rollback.intent, moves app.db with its -wal, -shm and -journal into rollback-<time>/,
// copies the snapshot to app.db.tmp, verifies it, renames it to app.db (never beside a WAL,
// which SQLite would replay into it), moves the snapshot into the folder, and removes
// migrate.intent, then rollback.intent. With no snapshot yet (the migration stopped while
// taking it), app.db is still at from: it removes the snapshot's temp and migrate.intent.
// The recovery tables are in rollback_crash_test.go and ADR-0035.

// RollbackIntent is db rollback's durable intent in the state directory. While it is there,
// Open refuses (ErrRollbackInterrupted).
const RollbackIntent = "rollback.intent"

var (
	// ErrRollbackRefused: db rollback isn't possible here; nothing was changed.
	ErrRollbackRefused = errors.New("sql: db rollback refused; nothing was changed")
	// ErrRollbackInterference: an interrupted rollback's files don't match its intent, which
	// only a writer ignoring the instance lock, or a hand edit, can cause. It stops, keeps its
	// intent, and changes nothing more.
	ErrRollbackInterference = errors.New("sql: an interrupted db rollback's files don't match its intent; " +
		"rollback.intent stays and nothing more was changed; follow the upgrade runbook (ADR-0035)")
	// ErrRollbackInterrupted: Open found rollback.intent. Nothing was opened or created.
	ErrRollbackInterrupted = errors.New("sql: a db rollback was interrupted (rollback.intent is in the " +
		"state directory); run postelmd db rollback again to finish it (ADR-0035)")
)

// RolledBack is what Rollback did: the migration from → to it undid, the folder (in the
// state directory) the moved files are in, and whether it restored the snapshot (false: the
// migration had no snapshot yet, so only its intent was retired).
type RolledBack struct {
	From, To int
	Dir      string
	Restored bool
}

// rollbackIntent is rollback.intent's content.
type rollbackIntent struct {
	From     int    `json:"from"`
	To       int    `json:"to"`
	Snapshot string `json:"snapshot"`
	Dir      string `json:"dir"`
}

var rollbackDirPattern = regexp.MustCompile(`^rollback-[0-9]{8}T[0-9]{6}Z$`)

// rollbackDirName is the folder the moved files go to: rollback-<UTC time to the second>.
func rollbackDirName(now time.Time) string {
	return "rollback-" + now.UTC().Format("20060102T150405Z")
}

// dbFiles are app.db and every file SQLite opens beside it, in the order they move.
var dbFiles = []string{AppDB, AppDB + "-wal", AppDB + "-shm", AppDB + "-journal"}

// Rollback runs db rollback on stateDir with this build's migrations. The caller holds the
// instance lock and has passed the data format check (CLAUDE.md rule 12); now names the
// rollback folder.
func Rollback(ctx context.Context, stateDir string, now time.Time) (RolledBack, error) {
	return rollback(ctx, stateDir, migrationFiles, now)
}

func rollback(ctx context.Context, stateDir string, fsys fs.FS, now time.Time) (RolledBack, error) {
	if err := checkAbs(stateDir); err != nil {
		return RolledBack{}, err
	}
	ms, err := loadMigrations(fsys)
	if err != nil {
		return RolledBack{}, err
	}
	rb, err := readRollbackIntent(stateDir)
	if err != nil {
		return RolledBack{}, err
	}
	if rb != nil {
		if rb.To != len(ms) {
			return RolledBack{}, fmt.Errorf("%w (rollback.intent names %d → %d; this build's last migration is %d)",
				ErrRollbackInterference, rb.From, rb.To, len(ms))
		}
		return continueRollback(ctx, stateDir, *rb)
	}
	rb = &rollbackIntent{Dir: rollbackDirName(now)}
	restore, err := checkEligible(ctx, stateDir, len(ms), rb)
	if err != nil {
		return RolledBack{}, fmt.Errorf("%w: %w", ErrRollbackRefused, err)
	}
	if !restore {
		return retire(stateDir, rb.From, rb.To)
	}
	b, err := json.Marshal(rb)
	if err != nil {
		return RolledBack{}, err
	}
	if err := writeSidecar(stateDir, RollbackIntent, append(b, '\n'), ErrRollbackInterference,
		"rollback-intent-synced", "rollback-intent-written"); err != nil {
		return RolledBack{}, err
	}
	return continueRollback(ctx, stateDir, *rb)
}

// readRollbackIntent reads rollback.intent: nil if absent; ErrRollbackInterference if it
// isn't one this command could have written.
func readRollbackIntent(stateDir string) (*rollbackIntent, error) {
	b, err := readSidecar(stateDir, RollbackIntent, ErrRollbackInterference)
	if err != nil || b == nil {
		return nil, err
	}
	var rb rollbackIntent
	if err := decodeStrict(b, &rb); err != nil {
		return nil, fmt.Errorf("%w (rollback.intent doesn't parse)", ErrRollbackInterference)
	}
	if rb.From < 1 || rb.To <= rb.From || rb.Snapshot != snapshotName(rb.From, rb.To) || !rollbackDirPattern.MatchString(rb.Dir) {
		return nil, fmt.Errorf("%w (rollback.intent names %d → %d)", ErrRollbackInterference, rb.From, rb.To)
	}
	return &rb, nil
}

// checkEligible reads, changing nothing, whether a rollback may run, and fills rb's
// migration from migrate.intent. It reports whether there is a snapshot to restore.
func checkEligible(ctx context.Context, stateDir string, last int, rb *rollbackIntent) (bool, error) {
	in, err := readIntent(stateDir)
	if err != nil {
		return false, err
	}
	if in == nil {
		if started, _ := filepath.Glob(filepath.Join(stateDir, "post-migration-started-*")); len(started) > 0 {
			return false, errors.New("no migration was interrupted, and the new version started after its last " +
				"migration (a post-migration-started file is there), so its database may hold changes the " +
				"snapshot lacks: nothing to roll back with this command; fix forward, or follow the upgrade runbook")
		}
		return false, errors.New("no migration was interrupted (no migrate.intent): nothing to roll back")
	}
	rb.From, rb.To, rb.Snapshot = in.From, in.To, in.Snapshot
	if in.To != last {
		return false, fmt.Errorf("migrate.intent names %d → %d, but this build's last migration is %d: "+
			"run db rollback with the build that started that migration", in.From, in.To, last)
	}
	if ok, err := regularFile(filepath.Join(stateDir, markerName(in.From, in.To))); ok || err != nil {
		return false, errors.Join(fmt.Errorf("%s is there: the new version started after the migration", markerName(in.From, in.To)), err)
	}
	uv, err := readForRollback(ctx, stateDir, in)
	if err != nil {
		return false, err
	}
	snap := filepath.Join(stateDir, in.Snapshot)
	var have [4]bool
	for i, p := range []string{snap, snap + ".tmp", snap + ".tmp-journal", filepath.Join(stateDir, AppDB+".tmp")} {
		if have[i], err = regularFile(p); err != nil {
			return false, err
		}
	}
	switch {
	case have[3]:
		return false, errors.New("an app.db.tmp is in the state directory; move it aside")
	case !have[0]:
		if uv != in.From {
			return false, fmt.Errorf("the snapshot is missing, but app.db is at version %d, not %d", uv, in.From)
		}
		return false, nil
	case have[1] || have[2]:
		return false, errors.New("a snapshot temp is beside the snapshot")
	}
	if err := verifySnapshot(ctx, snap, in.From); err != nil {
		// Not the cause's text: ErrDamaged's fix (move app.db aside) is for a start, and
		// would leave migrate.intent behind, so the previous build would still refuse.
		return false, errNoRestore("the pre-migration snapshot doesn't verify")
	}
	if _, err := os.Lstat(filepath.Join(stateDir, rb.Dir)); !errors.Is(err, fs.ErrNotExist) {
		return false, errors.Join(fmt.Errorf("%s is already there; run this again in a second", rb.Dir), err)
	}
	return true, nil
}

// readForRollback checks app.db and the files beside it (regular files only: a FIFO would
// block SQLite's open), then reads app.db's committed state on a read-only connection: its
// version must be one of the intent's, and the post_migration_started row must be absent.
func readForRollback(ctx context.Context, stateDir string, in *intent) (int, error) {
	path := filepath.Join(stateDir, AppDB)
	if _, err := checkRegular(path); err != nil {
		return 0, err
	}
	var beside [3]bool
	for i, n := range dbFiles[1:] {
		var err error
		if beside[i], err = regularFile(filepath.Join(stateDir, n)); err != nil {
			return 0, err
		}
	}
	if beside[2] {
		return 0, errors.New("an app.db-journal is beside app.db, which no PostelMD step leaves; move it aside")
	}
	// With no WAL, immutable=1 reads the file alone and creates no -wal or -shm; with one,
	// the committed transactions in it count, so the read goes through it (SQLite may update
	// the -shm index, never the database or the WAL).
	q := copyQuery + "&immutable=1"
	if beside[0] {
		q = copyQuery
	}
	p, err := openPool(path, q, 1)
	if err != nil {
		return 0, errNoRestore("app.db can't be read")
	}
	defer func() { _ = p.Close() }()
	var uv, rows int
	if err := p.QueryRowContext(ctx, "PRAGMA user_version").Scan(&uv); err != nil {
		return 0, errNoRestore("app.db can't be read")
	}
	if uv < in.From || uv > in.To {
		return 0, fmt.Errorf("app.db is at version %d, outside migrate.intent's %d → %d", uv, in.From, in.To)
	}
	if err := p.QueryRowContext(ctx, "SELECT count(*) FROM meta WHERE key = ?", markerKey(in.From, in.To)).Scan(&rows); err != nil {
		return 0, errNoRestore("app.db can't be read")
	}
	if rows != 0 {
		return 0, errors.New("app.db has the post_migration_started row: the new version started after the migration")
	}
	return uv, nil
}

// errNoRestore is a refusal where nothing this command can restore is left: the fix is to
// go forward, not back.
func errNoRestore(what string) error {
	return fmt.Errorf("%s: fix forward with a newer image, or follow the upgrade runbook", what)
}
