//go:build linux

package intents

import (
	"context"
	"fmt"
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

// Resolve ends the flagged intent opID: the operator decided (ADR-0044; postelmd intents
// resolve, with the server stopped and the vault checked). A save's temp is removed only once
// a copy of it is durable in recovery/<op-id>/ (a copy with the same bytes is that copy;
// otherwise the temp is copied first), so no edit is lost; then the intent's row goes
// (sqlstore.DB.Resolve), and the next scan decides its paths as any others. The copies stay.
//
// Refused, changing nothing: an op ID no intent can have (sqlstore.ErrBadIntent), an open
// intent (sqlstore.ErrIntentOpen: the start's recovery owns it), and any kind but a save
// (sqlstore.ErrResolveKind; each later slice extends resolve for its kind). intent.resolving is audited before the transaction and
// intent.resolved after it commits. Each step leaves a flagged intent whose temp is there or
// gone with its copy kept, so a crash anywhere is undone by running it again.
func Resolve(ctx context.Context, r Recovery, opID string) (Resolution, error) {
	var res Resolution
	if !sqlstore.ValidOpID(sqlstore.IntentDelete, opID) { // the loosest format any kind's op ID has
		return res, fmt.Errorf("%w: no intent has an op ID like that", sqlstore.ErrBadIntent)
	}
	err := r.Vault.Locked(ctx, func() error {
		in, err := find(ctx, r.DB, opID)
		switch {
		case err != nil:
			return err
		case !sqlstore.ValidOpID(in.Kind, opID):
			return fmt.Errorf("%w: a %s op ID", sqlstore.ErrBadIntent, in.Kind)
		case !sqlstore.Resolvable(in.Kind):
			return sqlstore.ErrResolveKind
		case !in.Flagged:
			return sqlstore.ErrIntentOpen
		}
		res.Kind = in.Kind
		if err := r.removeTempKept(ctx, opID, &res); err != nil {
			return err
		}
		attrs := []slog.Attr{slog.String("op_id", opID), slog.String("kind", in.Kind), slog.Bool("temp_removed", res.TempRemoved)}
		r.Audit.Record(ctx, "intent.resolving", attrs...)
		if err := r.DB.Resolve(ctx, opID); err != nil {
			return err
		}
		r.Audit.Record(ctx, "intent.resolved", attrs...)
		res.Copies = copiesOf(r.State, opID)
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
