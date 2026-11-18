//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
)

// TestRootsOfEachKind: the paths an intent names, each with everything under it, which the
// scan leaves alone while the intent is open or flagged (ADR-0033, "The scan and intents").
func TestRootsOfEachKind(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	b := rows["notes/b.md"]
	must(t, db.Begin(ctx, opA, aSave(rows["a.md"]), t0))
	must(t, db.Begin(ctx, opB, MoveIntent{From: "notes/b.md", To: "c.md", Device: 2049, Inode: b.Stat.Inode,
		Files: []Affected{affected(b)}, Mechanism: MechanismNoReplace}, t0))
	must(t, db.BeginDelete(ctx, stamp, folderPayload(ctx, db), folderItem, t0))
	want := map[string][]string{opA: {"a.md"}, opB: {"notes/b.md", "c.md"}, stamp: {"notes"}}
	in, err := db.Intents(ctx)
	must(t, err)
	for _, i := range in {
		got, err := i.Roots()
		must(t, err)
		if !slices.Equal(got, want[i.OpID]) {
			t.Errorf("%s intent's roots = %q, want %q", i.Kind, got, want[i.OpID])
		}
	}
}

func TestRootsOfABadPayload(t *testing.T) {
	db, _ := assigned(t)
	ctx := context.Background()
	must(t, db.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO intents (op_id, kind, state, payload, at_ms) VALUES (?, 'save', 'flagged', '{}', 0)", opA)
		return err
	}))
	if _, err := onlyIntent(t, db).Roots(); !errors.Is(err, ErrBadIntent) {
		t.Fatalf("Roots of a bad payload = %v, want ErrBadIntent", err)
	}
}
