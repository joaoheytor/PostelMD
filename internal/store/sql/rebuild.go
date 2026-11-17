//go:build linux

package sql

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"
)

// postelmd db rebuild's state-directory steps (ADR-0030, mechanics in ADR-0040): its durable
// intent, moving the old database aside, keeping the vault's temps in recovery/, and the
// open that creates the new database while the intent is there. internal/ids runs the steps
// in order; each is safe to run again after a crash. The recovery tables are in ADR-0040,
// rebuild_crash_test.go and internal/ids's rebuild crash test.

// RebuildIntent is db rebuild's durable intent in the state directory. While it is there,
// Open refuses (ErrRebuildInterrupted).
const RebuildIntent = "rebuild.intent"

var (
	// ErrRebuildRefused: db rebuild can't start here; nothing was changed.
	ErrRebuildRefused = errors.New("sql: db rebuild refused; nothing was changed")
	// ErrRebuildInterference: a rebuild's files don't match its intent, which only a writer
	// ignoring the instance lock, or a hand edit, can cause. The run stops where it found it
	// and keeps its intent; steps it finished before stay done (its log says which).
	ErrRebuildInterference = errors.New("sql: db rebuild's files don't match its intent; it stopped " +
		"there and rebuild.intent stays; follow the runbook (ADR-0040)")
	// ErrRebuildInterrupted: Open found rebuild.intent. Nothing was opened or created.
	ErrRebuildInterrupted = errors.New("sql: a db rebuild was interrupted (rebuild.intent is in the " +
		"state directory); run postelmd db rebuild again to finish it (ADR-0040)")
)

// Rebuild is rebuild.intent's content: the rebuild UUID, its time (UTC, to the second),
// whether app.db existed when it started, and the files step 2 moves into the damaged folder
// (the original move): the database's files there, and with Migration, an interrupted
// migration's intent, snapshot and post-migration-started files.
type Rebuild struct {
	UUID      string   `json:"uuid"`
	Time      string   `json:"time"`
	HadDB     bool     `json:"had_db"`
	Moves     []string `json:"moves"`
	Migration bool     `json:"migration,omitempty"`
}

// migrationLeftover is a file of an interrupted migration that step 2 moves with Migration.
var migrationLeftover = regexp.MustCompile(`^(migrate\.intent|pre-migrate-[0-9]+-[0-9]+\.db(\.tmp(-journal)?)?|post-migration-started-[0-9]+-[0-9]+)$`)

func (r Rebuild) stamp() string {
	t, _ := time.Parse(time.RFC3339, r.Time) // checked when read or written
	return t.UTC().Format("20060102T150405Z")
}

// DamagedDir is the folder in the state directory the old database moves to.
func (r Rebuild) DamagedDir() string { return "damaged-" + r.stamp() }

// RecoveryDir is the folder in the state directory the vault's temps are kept in.
func (r Rebuild) RecoveryDir() string { return filepath.Join("recovery", "rebuild-"+r.stamp()) }

func (r Rebuild) valid() bool {
	t, err := time.Parse(time.RFC3339, r.Time)
	if !uuidPattern.MatchString(r.UUID) || err != nil || t.UTC().Format(time.RFC3339) != r.Time ||
		r.HadDB != slices.Contains(r.Moves, AppDB) {
		return false
	}
	seen := map[string]bool{}
	for _, n := range r.Moves {
		known := slices.Contains(dbFiles, n) || r.Migration && migrationLeftover.MatchString(n)
		if seen[n] || !known {
			return false
		}
		seen[n] = true
	}
	return true
}

// NewUUID is a random version-4 UUID, lower case (crypto/rand.Read never fails).
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// ReadRebuildIntent reads rebuild.intent: nil if absent; ErrRebuildInterference if it isn't
// one BeginRebuild could have written.
func ReadRebuildIntent(stateDir string) (*Rebuild, error) {
	if err := checkAbs(stateDir); err != nil {
		return nil, err
	}
	b, err := readSidecar(stateDir, RebuildIntent, ErrRebuildInterference)
	if err != nil || b == nil {
		return nil, err
	}
	var r Rebuild
	if err := decodeStrict(b, &r); err != nil || !r.valid() {
		return nil, fmt.Errorf("%w (rebuild.intent doesn't parse)", ErrRebuildInterference)
	}
	return &r, nil
}

// BeginRebuild checks, changing nothing, that a rebuild may start, then makes rebuild.intent
// durable: the rebuild's first side effect (CLAUDE.md rule 14). The caller holds the
// instance lock and found no intent (ReadRebuildIntent).
//
// An interrupted migration that db rollback can undo (or retire) refuses, as does
// rollback.intent: that recovery comes first (ADR-0035). One it can't (app.db missing or
// unreadable, or the new version started) leaves db rebuild as the way out, so its files
// join the original move.
func BeginRebuild(ctx context.Context, stateDir string, now time.Time) (Rebuild, error) {
	return beginRebuild(ctx, stateDir, now, migrationFiles)
}

func beginRebuild(ctx context.Context, stateDir string, now time.Time, fsys fs.FS) (Rebuild, error) {
	if err := checkAbs(stateDir); err != nil {
		return Rebuild{}, err
	}
	refuse := func(err error) (Rebuild, error) { return Rebuild{}, fmt.Errorf("%w: %w", ErrRebuildRefused, err) }
	if ok, err := regularFile(filepath.Join(stateDir, RollbackIntent)); ok || err != nil {
		return refuse(errors.Join(errors.New("rollback.intent is in the state directory: run db rollback first (ADR-0035)"), err))
	}
	r := Rebuild{UUID: NewUUID(), Time: now.UTC().Truncate(time.Second).Format(time.RFC3339)}
	for _, n := range dbFiles {
		ok, err := regularFile(filepath.Join(stateDir, n))
		if err != nil {
			return refuse(err)
		}
		if ok {
			r.Moves = append(r.Moves, n)
		}
	}
	r.HadDB = slices.Contains(r.Moves, AppDB)
	if ok, err := regularFile(filepath.Join(stateDir, MigrateIntent)); err != nil {
		return refuse(err)
	} else if ok {
		ms, err := loadMigrations(fsys)
		if err != nil {
			return Rebuild{}, err
		}
		_, err = checkEligible(ctx, stateDir, len(ms), &rollbackIntent{Dir: rollbackDirName(now)})
		var past pastRollback
		switch {
		case err == nil:
			return refuse(errors.New("a migration was interrupted (migrate.intent), and db rollback can undo it: run db rollback first (ADR-0035)"))
		case !errors.As(err, &past):
			return refuse(fmt.Errorf("a migration was interrupted (migrate.intent), and db rollback refuses it: %w", err))
		}
		left, err := migrationLeftovers(stateDir)
		if err != nil {
			return refuse(err)
		}
		r.Migration, r.Moves = true, append(r.Moves, left...)
	}
	if _, err := os.Lstat(filepath.Join(stateDir, r.DamagedDir())); !errors.Is(err, fs.ErrNotExist) {
		return refuse(errors.Join(fmt.Errorf("%s is already there; run this again in a second", r.DamagedDir()), err))
	}
	b, err := json.Marshal(r)
	if err != nil {
		return Rebuild{}, err
	}
	err = writeSidecar(stateDir, RebuildIntent, append(b, '\n'), ErrRebuildInterference, "rebuild-intent-synced", "rebuild-intent-written")
	return r, err
}

// migrationLeftovers are an interrupted migration's files in stateDir, in name order.
func migrationLeftovers(stateDir string) ([]string, error) {
	es, err := os.ReadDir(stateDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		if !migrationLeftover.MatchString(e.Name()) {
			continue
		}
		if ok, err := regularFile(filepath.Join(stateDir, e.Name())); err != nil {
			return nil, err
		} else if ok {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// FinishRebuild removes rebuild.intent durably: the rebuild's last step.
func FinishRebuild(stateDir string) error {
	return removeSidecar(stateDir, RebuildIntent, "rebuild-done")
}

// OpenForRebuild is Open for db rebuild's step 4, while its intent is there: it creates (or
// opens the half-built) app.db, checks and migrates it. The caller has run
// MoveAsideForRebuild for this intent.
func OpenForRebuild(ctx context.Context, stateDir string, log *slog.Logger) (*DB, error) {
	return openState(ctx, stateDir, log, migrationFiles, true)
}

// rebuilding refuses an open while rebuild.intent is there, unless the rebuild opens.
func rebuilding(stateDir string, forRebuild bool) error {
	if forRebuild {
		return nil
	}
	if ok, err := regularFile(filepath.Join(stateDir, RebuildIntent)); ok || errors.Is(err, errNotRegular) {
		return ErrRebuildInterrupted
	} else if err != nil {
		return err
	}
	return nil
}

// rebuildInterference wraps what a step found as ErrRebuildInterference.
func rebuildInterference(err error) error { return fmt.Errorf("%w: %w", ErrRebuildInterference, err) }

// mkdirDurable creates path (0700) and fsyncs its parent, unless it is a folder already.
func mkdirDurable(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case err == nil && fi.IsDir():
		return nil
	case err == nil:
		return rebuildInterference(fmt.Errorf("%s isn't a folder", filepath.Base(path)))
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("sql: create %s: %w", filepath.Base(path), err)
	}
	return syncDir(path)
}
