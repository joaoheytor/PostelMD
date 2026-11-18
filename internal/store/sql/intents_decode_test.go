//go:build linux

package sql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestDecodeIsStrict: a payload the store didn't write exactly as its type is ErrBadIntent,
// never read as the nearest valid value (ADR-0033: payloads are decoded strictly).
func TestDecodeIsStrict(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	b, err := json.Marshal(aSave(rows["a.md"]))
	must(t, err)
	valid := string(b)
	open := strings.TrimSuffix(valid, "}")
	for name, tc := range map[string]struct {
		payload string
		ok      bool
	}{
		"as written":            {valid, true},
		"trailing whitespace":   {valid + " \n", true},
		"an unknown field":      {open + `,"Extra":1}`, false},
		"a second value":        {valid + ` {}`, false},
		"a stray closing brace": {valid + `}`, false},
		"a missing field":       {`{"file_id":"` + rows["a.md"].ID + `"}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			must(t, db.writeTx(ctx, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, "DELETE FROM intents"); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, "INSERT INTO intents (op_id, kind, state, payload, at_ms) VALUES (?, 'save', 'open', ?, 0)",
					opA, tc.payload)
				return err
			}))
			var p SaveIntent
			err := onlyIntent(t, db).Decode(&p)
			if tc.ok && err != nil {
				t.Fatalf("Decode = %v; want it read", err)
			}
			if !tc.ok && !errors.Is(err, ErrBadIntent) {
				t.Fatalf("Decode = %v (%+v); want ErrBadIntent", err, p)
			}
		})
	}
}
