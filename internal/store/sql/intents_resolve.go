//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrIntentOpen: the intent is open, so the start's recovery owns it; only a flagged one is
// the operator's to resolve. Nothing was changed.
var ErrIntentOpen = errors.New("sql: the intent is open, so the next start's recovery ends it; only a flagged intent can be resolved")

// Resolve ends a flagged intent: the operator decided (ADR-0033, postelmd intents resolve).
// What began with it is undone as a drop undoes it (a delete's moving record is removed: a
// trash entry its move left is one without a record, which stays for the operator; a
// restore's record goes back to trashed), and the paths it names are the scan's again. An
// open intent is ErrIntentOpen, an op ID with no intent ErrNoIntent; neither changes
// anything. The caller handles the intent's temp first.
func (db *DB) Resolve(ctx context.Context, opID string) error {
	return db.intentTx(ctx, "resolve", func(tx *sql.Tx) error {
		in := Intent{OpID: opID}
		var state, payload string
		err := tx.QueryRowContext(ctx, "SELECT kind, state, payload FROM intents WHERE op_id = ?", opID).Scan(&in.Kind, &state, &payload)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w (no intent has that op ID)", ErrNoIntent)
		case err != nil:
			return fmt.Errorf("sql: read an intent: %w", err)
		case state != "flagged":
			return ErrIntentOpen
		}
		in.payload = []byte(payload)
		if err := undoBegun(ctx, tx, in); err != nil {
			return err
		}
		return staleAs(one(ctx, tx, "resolve an intent", "DELETE FROM intents WHERE op_id = ? AND state = 'flagged'", opID), ErrNoIntent)
	})
}
