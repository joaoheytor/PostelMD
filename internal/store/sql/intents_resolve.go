//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

var (
	// ErrIntentOpen: the intent is open, so the start's recovery owns it; only a flagged one is
	// the operator's to resolve. Nothing was changed.
	ErrIntentOpen = errors.New("sql: the intent is open, so the next start's recovery ends it; only a flagged intent can be resolved")
	// ErrResolveKind: the intent's kind began more than its row, and this build can't end it
	// safely (ADR-0044). Nothing was changed.
	ErrResolveKind = errors.New("sql: this kind of intent can't be resolved by this build; see the runbook (Flagged intents: list and resolve)")
)

// Resolvable reports whether this build's resolve ends a flagged intent of kind (ADR-0044):
// a save, or a kind it doesn't know (a later build's), whose row is all it can end. Create,
// move, delete and restore began more than a row; each later slice extends resolve for its
// kind with that kind's rule.
func Resolvable(kind string) bool {
	switch kind {
	case IntentCreate, IntentMove, IntentDelete, IntentRestore:
		return false
	}
	return true
}

// ValidOpID reports whether opID has the format kind's op IDs have (32 lower-case hex digits,
// or a delete's trash stamp) and no control character, which a terminal would act on; list
// prints one that hasn't as <invalid op ID>.
func ValidOpID(kind, opID string) bool {
	return checkOpID(kind, opID) == nil && !strings.ContainsFunc(opID, unicode.IsControl)
}

// Resolve ends a flagged intent: the operator decided (ADR-0044, postelmd intents resolve).
// Only its row goes: Resolvable kinds began nothing else. The paths it names are the scan's
// again. A bad op ID is ErrBadIntent, an open intent ErrIntentOpen, another kind
// ErrResolveKind, no intent ErrNoIntent; none changes anything. The caller handles the
// intent's temp first.
func (db *DB) Resolve(ctx context.Context, opID string) error {
	if !ValidOpID(IntentDelete, opID) {
		return fmt.Errorf("%w: no intent has an op ID like that", ErrBadIntent)
	}
	return db.intentTx(ctx, "resolve", func(tx *sql.Tx) error {
		var kind, state string
		err := tx.QueryRowContext(ctx, "SELECT kind, state FROM intents WHERE op_id = ?", opID).Scan(&kind, &state)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w (no intent has that op ID)", ErrNoIntent)
		case err != nil:
			return fmt.Errorf("sql: read an intent: %w", err)
		case !ValidOpID(kind, opID):
			return fmt.Errorf("%w: a %s op ID", ErrBadIntent, kind)
		case !Resolvable(kind):
			return ErrResolveKind
		case state != "flagged":
			return ErrIntentOpen
		}
		return staleAs(one(ctx, tx, "resolve an intent", "DELETE FROM intents WHERE op_id = ? AND state = 'flagged'", opID), ErrNoIntent)
	})
}
