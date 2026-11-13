//go:build linux

package sql

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// The pre-migration sidecars in the state directory (ADR-0015, "Snapshot crash safety").
const (
	// MigrateIntent is the migration's durable intent: it can't live in the database being
	// migrated, so it is a sidecar file, written and fsynced before the snapshot.
	MigrateIntent = "migrate.intent"
	intentMax     = 4096 // a larger file isn't ours
)

// ErrMigrateInterference: an interrupted migration's intent doesn't match the database or its
// snapshot (ADR-0015's "anything else" row), or a snapshot this migration would take already
// exists without an intent. Found before the migration's first side effect: nothing was
// changed; the admin uses the upgrade runbook. Found later: ErrMigrateLateInterference.
var ErrMigrateInterference = errors.New("sql: an interrupted migration's state (migrate.intent, " +
	"the database's version or the pre-migration snapshot) doesn't match; nothing was changed; " +
	"follow the upgrade runbook (ADR-0015)")

// ErrMigrateLateInterference: a migration file changed after the migration's first side
// effect, which only a writer ignoring the instance lock can do. Something may have changed,
// so it doesn't say otherwise; errors.Is(err, ErrMigrateInterference) still holds.
var ErrMigrateLateInterference error = lateInterference{}

type lateInterference struct{}

func (lateInterference) Error() string {
	return "sql: a migration file changed while the migration ran; the migration's intent stays " +
		"in place; follow the upgrade runbook (ADR-0015)"
}

func (lateInterference) Unwrap() error { return ErrMigrateInterference }

// intent is migrate.intent's content: from, to, and the snapshot's path relative to the
// state directory (always snapshotName(from, to); recovery refuses anything else).
type intent struct {
	From     int    `json:"from"`
	To       int    `json:"to"`
	Snapshot string `json:"snapshot"`
}

// snapshotName is ADR-0015's pre-migrate-<from>-<to>.db.
func snapshotName(from, to int) string { return fmt.Sprintf("pre-migrate-%d-%d.db", from, to) }

// markerName is the post_migration_started sidecar of one migration (from → to).
func markerName(from, to int) string { return fmt.Sprintf("post-migration-started-%d-%d", from, to) }

// markerKey is the post_migration_started row of one migration in meta.
func markerKey(from, to int) string { return fmt.Sprintf("post_migration_started:%d-%d", from, to) }

// readIntent reads stateDir/migrate.intent: nil if absent. A file that isn't a regular file,
// or doesn't parse as an intent this runner could have written, is ErrMigrateInterference.
func readIntent(stateDir string) (*intent, error) {
	f, err := os.OpenFile(filepath.Join(stateDir, MigrateIntent), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: the state directory's own file
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%w (the intent is a symlink)", ErrMigrateInterference)
	}
	if err != nil {
		return nil, intentIOError(err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, intentIOError(err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > intentMax {
		return nil, fmt.Errorf("%w (the intent isn't a small regular file)", ErrMigrateInterference)
	}
	b, err := io.ReadAll(io.LimitReader(f, intentMax+1))
	if err != nil {
		return nil, intentIOError(err)
	}
	if len(b) > intentMax { // it grew since the Stat
		return nil, fmt.Errorf("%w (the intent isn't a small regular file)", ErrMigrateInterference)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var in intent
	if err := dec.Decode(&in); err != nil || dec.More() {
		return nil, fmt.Errorf("%w (the intent doesn't parse)", ErrMigrateInterference)
	}
	if in.From < 1 || in.To <= in.From || in.Snapshot != snapshotName(in.From, in.To) {
		return nil, fmt.Errorf("%w (the intent names %d → %d)", ErrMigrateInterference, in.From, in.To)
	}
	return &in, nil
}

// intentIOError is an I/O error reading the intent, which isn't interference: it keeps the
// operation and the cause, and names the file by its fixed name instead of its path.
func intentIOError(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = &fs.PathError{Op: pe.Op, Path: MigrateIntent, Err: pe.Err}
	}
	return fmt.Errorf("sql: read the migration intent: %w", err)
}

// writeIntent makes stateDir/migrate.intent durable before the migration's first side
// effect (CLAUDE.md rule 14): written to a temp (a stale one from a crash is removed first),
// fsynced, renamed into place and the folder fsynced, so the intent is either absent or whole.
func writeIntent(stateDir string, in intent) error {
	path := filepath.Join(stateDir, MigrateIntent)
	tmp := path + ".tmp"
	if err := removeRegular(tmp); err != nil {
		return err
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	f, err := createExclusive(tmp, "migration intent")
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("sql: write the migration intent: %w", err)
	}
	killPoint("intent-synced")
	// The caller found no intent under the instance lock: RENAME_NOREPLACE makes sure.
	if err := renameNoReplace(tmp, path); errors.Is(err, os.ErrExist) {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w (an intent appeared at its name)", ErrMigrateInterference)
	} else if err != nil {
		return fmt.Errorf("sql: write the migration intent: %w", err)
	}
	if err := syncDir(path); err != nil {
		return fmt.Errorf("sql: write the migration intent: %w", err)
	}
	killPoint("intent-written")
	return nil
}

// removeIntent removes migrate.intent durably: the last step of a migration.
func removeIntent(stateDir string) error {
	path := filepath.Join(stateDir, MigrateIntent)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("sql: remove the migration intent: %w", err)
	}
	killPoint("intent-removed")
	return syncDir(path)
}

// present reports whether path exists as a regular file. Anything else there (a symlink, a
// folder) is ErrMigrateInterference: none of this runner's files is ever one.
func present(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%w (a migration file isn't a regular file: %s)", ErrMigrateInterference, fi.Mode().Type())
	}
	return true, nil
}

// removeRegular removes path if it is a regular file; absent is fine, anything else refuses.
func removeRegular(path string) error {
	ok, err := present(path)
	if err != nil || !ok {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("sql: remove a partial migration file: %w", err)
	}
	return nil
}
