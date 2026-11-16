//go:build linux

package sql

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The pre-migration sidecars in the state directory (ADR-0015, "Snapshot crash safety").

// MigrateIntent is the migration's durable intent: it can't live in the database being
// migrated, so it is a sidecar file, written and fsynced before the snapshot.
const MigrateIntent = "migrate.intent"

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

// asLate turns a plain ErrMigrateInterference into ErrMigrateLateInterference, for a refusal
// found after a side effect: the plain error's "nothing was changed" text goes, its detail
// and its other causes (ErrCopySidecar, ErrBadCopy, …) stay. Other errors pass unchanged.
func asLate(err error) error {
	if !errors.Is(err, ErrMigrateInterference) || errors.Is(err, ErrMigrateLateInterference) {
		return err
	}
	detail, ok := strings.CutPrefix(err.Error(), ErrMigrateInterference.Error())
	if !ok || strings.Contains(detail, "nothing was changed") {
		detail = ""
	}
	return lateCause{cause: err, detail: detail}
}

// lateCause is asLate's error: ErrMigrateLateInterference's text and the cause's detail.
type lateCause struct {
	cause  error
	detail string
}

func (e lateCause) Error() string   { return ErrMigrateLateInterference.Error() + e.detail }
func (e lateCause) Unwrap() []error { return []error{ErrMigrateLateInterference, e.cause} }

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
	b, err := readSidecar(stateDir, MigrateIntent, ErrMigrateInterference)
	if err != nil || b == nil {
		return nil, err
	}
	var in intent
	if err := decodeStrict(b, &in); err != nil {
		return nil, fmt.Errorf("%w (the intent doesn't parse)", ErrMigrateInterference)
	}
	if in.From < 1 || in.To <= in.From || in.Snapshot != snapshotName(in.From, in.To) {
		return nil, fmt.Errorf("%w (the intent names %d → %d)", ErrMigrateInterference, in.From, in.To)
	}
	return &in, nil
}

// decodeStrict decodes one JSON value from b into v: no unknown fields, nothing after it.
func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("sql: data after the value")
	}
	return nil
}

// writeIntent makes stateDir/migrate.intent durable before the migration's first side
// effect (CLAUDE.md rule 14), by writeSidecar: the intent is either absent or whole.
func writeIntent(stateDir string, in intent) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return writeSidecar(stateDir, MigrateIntent, append(b, '\n'), ErrMigrateInterference, "intent-synced", "intent-written")
}

// removeIntent removes migrate.intent durably: the last step of a migration.
func removeIntent(stateDir string) error {
	return removeSidecar(stateDir, MigrateIntent, "intent-removed")
}

// errNotRegular: something other than a regular file is where one of the state directory's
// own files belongs.
var errNotRegular = errors.New("sql: not a regular file")

// regularFile reports whether path is a regular file; anything else there (a symlink, a
// folder, a FIFO) is errNotRegular, naming the file and its type.
func regularFile(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%w: %s (%s)", errNotRegular, filepath.Base(path), fi.Mode().Type())
	}
	return true, nil
}

// present is regularFile for the migration runner: anything but a regular file is
// ErrMigrateInterference, since none of its files is ever one.
func present(path string) (bool, error) {
	ok, err := regularFile(path)
	if errors.Is(err, errNotRegular) {
		return false, fmt.Errorf("%w (%w)", ErrMigrateInterference, err)
	}
	return ok, err
}

// removeRegular removes path if it is a regular file; absent is fine, anything else refuses.
func removeRegular(path string) error {
	ok, err := present(path)
	if err != nil || !ok {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("sql: remove the leftover %s: %w", filepath.Base(path), err)
	}
	return nil
}
