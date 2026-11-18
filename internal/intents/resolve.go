//go:build linux

package intents

import (
	"context"
	"log/slog"
	"time"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// Resolution is what Resolve did.
type Resolution struct {
	Kind        string
	TempRemoved bool   // the intent's temp, copied into Copies first
	TempLeft    bool   // a temp that isn't a regular file: the next start's sweep removes it
	Copies      string // the intent's recovery/<op-id>/ folder, "" when it has none
}

// Resolve ends the flagged intent opID: the operator decided (ADR-0033's "until the operator
// decides"; postelmd intents resolve, with the server stopped). Its temp is removed only once
// a copy of it is durable in recovery/<op-id>/ (a copy with the same bytes is that copy;
// otherwise the temp is copied first), so no edit is lost; then the intent goes, with what
// began with it (sqlstore.DB.Resolve), and the next scan decides its paths as any others.
// The copies stay. An open intent is sqlstore.ErrIntentOpen: the start's recovery owns it.
// Each step leaves a flagged intent whose temp is there or gone with its copy kept, so a
// crash anywhere is undone by running it again.
func Resolve(ctx context.Context, r Recovery, opID string) (Resolution, error) {
	var res Resolution
	err := r.Vault.Locked(ctx, func() error {
		in, err := find(ctx, r.DB, opID)
		if err != nil {
			return err
		}
		if !in.Flagged {
			return sqlstore.ErrIntentOpen
		}
		res.Kind = in.Kind
		if in.Kind != sqlstore.IntentDelete { // a delete's op ID is a trash stamp: it has no temp
			if err := r.removeTempKept(ctx, opID, &res); err != nil {
				return err
			}
		}
		if err := r.DB.Resolve(ctx, opID); err != nil {
			return err
		}
		res.Copies = copiesOf(r.State, opID)
		r.Audit.Record(ctx, "intent.resolved", slog.String("op_id", opID), slog.String("kind", in.Kind),
			slog.Bool("temp_removed", res.TempRemoved))
		return nil
	})
	return res, err
}

// removeTempKept removes the intent's temp once its copy is durable.
func (r Recovery) removeTempKept(ctx context.Context, opID string, res *Resolution) error {
	P, err := r.Vault.JudgeTemp(ctx, opID)
	switch {
	case err != nil:
		return err
	case !P.Present:
		return nil
	case !P.Regular:
		res.TempLeft = true
		return nil
	}
	if err := sqlstore.KeepIntentCopy(r.State, opID, "temp", r.openTemp(opID)); err != nil {
		return err
	}
	if err := r.Vault.RemoveTemp(opID); err != nil {
		return err
	}
	res.TempRemoved = true
	return nil
}

// find returns the intent opID, open or flagged, or sqlstore.ErrNoIntent.
func find(ctx context.Context, db Store, opID string) (sqlstore.Intent, error) {
	all, err := db.Intents(ctx)
	if err != nil {
		return sqlstore.Intent{}, err
	}
	for _, in := range all {
		if in.OpID == opID {
			return in, nil
		}
	}
	return sqlstore.Intent{}, sqlstore.ErrNoIntent
}

// Listed is one intent as postelmd intents list shows it.
type Listed struct {
	OpID, Kind string
	State      string // "open" (the next start's recovery ends it) or "flagged" (the operator's)
	At         time.Time
	Copies     string // its recovery/<op-id>/ folder, "" when it has none
}

// List returns every intent, oldest first, with its copies' folder.
func List(ctx context.Context, db Store, stateDir string) ([]Listed, error) {
	all, err := db.Intents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Listed, 0, len(all))
	for _, in := range all {
		l := Listed{OpID: in.OpID, Kind: in.Kind, State: "open", At: in.At, Copies: copiesOf(stateDir, in.OpID)}
		if in.Flagged {
			l.State = "flagged"
		}
		out = append(out, l)
	}
	return out, nil
}

func copiesOf(stateDir, opID string) string { return sqlstore.IntentCopies(stateDir, opID) }
