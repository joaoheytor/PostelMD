//go:build linux

package sql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Intents (ADR-0033): a row per PostelMD write in flight, committed before the write's first
// filesystem step and deleted in the transaction that records its result. Each transaction
// here is one durable commit (synchronous=FULL); none touches the vault. The caller holds
// the vault mutex (ADR-0030's lock order), apart from the purge's record changes.

var (
	// ErrNoIntent: no open intent has that op ID (and that kind).
	ErrNoIntent = errors.New("sql: no open intent with that op ID")
	// ErrIntentStale: a row an intent names isn't what the intent recorded. Under the vault
	// mutex only a writer ignoring the instance lock can cause it; nothing was changed.
	ErrIntentStale = errors.New("sql: a file row isn't what the intent recorded; nothing was changed")
	// ErrTrashState: the trash record isn't in the state the step needs (a restore or purge of
	// a record that is purging or restoring answers 409, ADR-0033). Nothing was changed.
	ErrTrashState = errors.New("sql: the trash item is being purged or restored, or is gone")
	// ErrBadIntent: an op ID or payload the store would never write.
	ErrBadIntent = errors.New("sql: not a valid intent")
)

// The kinds of intent.
const (
	IntentSave    = "save"
	IntentCreate  = "create"
	IntentMove    = "move"
	IntentDelete  = "delete"
	IntentRestore = "restore"
)

// MechanismNoReplace is the one mechanism v0.2 uses: the vault refuses filesystems without
// RENAME_NOREPLACE, so link-unlink and rename-checked come with ADR-0006's fallbacks.
const MechanismNoReplace = "rename-noreplace"

// tempOpID is the op ID of every intent but a delete's: the temp's 32 hex digits.
var tempOpID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Payload is an intent's content: one of SaveIntent, CreateIntent, MoveIntent, DeleteIntent
// or RestoreIntent.
type Payload interface {
	kind() string
	valid() bool
}

// Intent is a row of intents. Decode reads its payload.
type Intent struct {
	OpID    string
	Kind    string
	Flagged bool // recovery found interference; the row stays until the operator decides
	At      time.Time
	payload []byte
}

// Decode reads the intent's payload into p, which must be of the intent's kind; unknown
// fields and anything after the value are refused.
func (in Intent) Decode(p Payload) error {
	if p.kind() != in.Kind {
		return fmt.Errorf("%w: a %s intent read as %s", ErrBadIntent, in.Kind, p.kind())
	}
	if err := decodeStrict(in.payload, p); err != nil || !p.valid() {
		return fmt.Errorf("%w: the %s payload doesn't parse", ErrBadIntent, in.Kind)
	}
	return nil
}

// checkOpID refuses an op ID the kind never has: 32 hex digits, or a delete's trash stamp (a
// name with no slash; the vault checks its layout).
func checkOpID(kind, opID string) error {
	ok := tempOpID.MatchString(opID)
	if kind == IntentDelete {
		ok = opID != "" && !strings.ContainsAny(opID, "/\x00") && len(opID) <= 255
	}
	if !ok {
		return fmt.Errorf("%w: a %s op ID", ErrBadIntent, kind)
	}
	return nil
}

// Intents returns every intent, open and flagged, oldest first.
func (db *DB) Intents(ctx context.Context) ([]Intent, error) {
	rows, err := db.r.QueryContext(ctx, "SELECT op_id, kind, state, payload, at_ms FROM intents ORDER BY at_ms, op_id")
	if err != nil {
		return nil, fmt.Errorf("sql: read the intents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Intent{}
	for rows.Next() {
		var in Intent
		var state, payload string
		var at int64
		if err := rows.Scan(&in.OpID, &in.Kind, &state, &payload, &at); err != nil {
			return nil, fmt.Errorf("sql: read the intents: %w", err)
		}
		in.Flagged, in.At, in.payload = state == "flagged", time.UnixMilli(at), []byte(payload)
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sql: read the intents: %w", err)
	}
	return out, nil
}

// Begin commits a save, create or move intent: the step before the operation's first
// filesystem change. A delete begins with BeginDelete and a restore with BeginRestore, which
// also change the trash record.
func (db *DB) Begin(ctx context.Context, opID string, p Payload, now time.Time) error {
	if k := p.kind(); k == IntentDelete || k == IntentRestore {
		return fmt.Errorf("%w: a %s intent begins with its trash record", ErrBadIntent, k)
	}
	return db.intentTx(ctx, p.kind()+"-begin", func(tx *sql.Tx) error { return insertIntent(ctx, tx, opID, p, now) })
}

func insertIntent(ctx context.Context, tx *sql.Tx, opID string, p Payload, now time.Time) error {
	if err := checkOpID(p.kind(), opID); err != nil {
		return err
	}
	if !p.valid() {
		return fmt.Errorf("%w: the %s payload", ErrBadIntent, p.kind())
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO intents (op_id, kind, state, payload, at_ms) VALUES (?, ?, 'open', ?, ?)`,
		opID, p.kind(), string(b), now.UnixMilli())
	if err != nil {
		return fmt.Errorf("sql: record an intent: %w", err)
	}
	return nil
}

// Drop ends an open intent whose operation didn't happen (its "not applied" rows): the row
// goes, and so does what began with it. A delete's moving trash record is removed; a
// restore's record goes back from restoring to trashed.
func (db *DB) Drop(ctx context.Context, opID string) error {
	return db.intentTx(ctx, "drop", func(tx *sql.Tx) error {
		in, err := openIntent(ctx, tx, opID, "")
		if err != nil {
			return err
		}
		switch in.Kind {
		case IntentDelete:
			err = one(ctx, tx, "drop a moving trash record", "DELETE FROM trash_records WHERE op_id = ? AND state = 'moving'", opID)
		case IntentRestore:
			var p RestoreIntent
			if err = in.Decode(&p); err == nil {
				err = setTrashState(ctx, tx, p.TrashOpID, "restoring", "trashed")
			}
		}
		if err != nil {
			return staleAs(err, ErrTrashState)
		}
		return deleteIntent(ctx, tx, opID)
	})
}

// Flag marks an open intent flagged: recovery found interference and kept both sides in
// recovery/<op-id>/ (ADR-0033). The row stays, with what began with it (a delete's moving
// record, a restore's restoring one, so neither can be restored or purged), and the sweep
// and the scan spare what it names.
func (db *DB) Flag(ctx context.Context, opID string) error {
	return db.intentTx(ctx, "flag", func(tx *sql.Tx) error {
		return staleAs(one(ctx, tx, "flag an intent", "UPDATE intents SET state = 'flagged' WHERE op_id = ? AND state = 'open'", opID), ErrNoIntent)
	})
}

// openIntent reads the open intent opID in tx, of kind if kind isn't "".
func openIntent(ctx context.Context, tx *sql.Tx, opID, kind string) (Intent, error) {
	in := Intent{OpID: opID}
	var payload string
	err := tx.QueryRowContext(ctx, "SELECT kind, payload FROM intents WHERE op_id = ? AND state = 'open'", opID).Scan(&in.Kind, &payload)
	if errors.Is(err, sql.ErrNoRows) || err == nil && kind != "" && in.Kind != kind {
		return Intent{}, ErrNoIntent
	}
	if err != nil {
		return Intent{}, fmt.Errorf("sql: read an intent: %w", err)
	}
	in.payload = []byte(payload)
	return in, nil
}

func deleteIntent(ctx context.Context, tx *sql.Tx, opID string) error {
	return staleAs(one(ctx, tx, "end an intent", "DELETE FROM intents WHERE op_id = ? AND state = 'open'", opID), ErrNoIntent)
}

// staleAs turns errStale (no row changed) into err.
func staleAs(err, as error) error {
	if errors.Is(err, errStale) {
		return as
	}
	return err
}

// intentTx runs fn in one write transaction with the kill points name-before-commit and
// name-after-commit, which the crash tests use (CLAUDE.md rule 14).
func (db *DB) intentTx(ctx context.Context, name string, fn func(*sql.Tx) error) error {
	err := db.writeTx(ctx, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		killPoint(name + "-before-commit")
		return nil
	})
	if err != nil {
		return err
	}
	killPoint(name + "-after-commit")
	return nil
}
