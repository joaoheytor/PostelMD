//go:build linux

// Package intents is intent recovery (ADR-0033): at start, after the first upgrade and
// before the sweep and the scan, every open intent is judged on the disk and the database
// and gets the one action its recovery table names: drop, drop keeping the temp, roll
// forward, or flag. It holds the vault mutex, then each transaction (ADR-0030's lock order).
// It logs op IDs and kinds, never a path or note content (CLAUDE.md rule 15).
package intents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/store/fs"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// Recovery is what Recover works on. The caller holds the instance lock and serves nothing
// yet (CLAUDE.md rule 12).
type Recovery struct {
	DB    Store
	Vault *vault.Vault
	State string // the state directory: its recovery/ holds the copies
	Log   *slog.Logger
	Audit *audit.Log
}

// Store is the database recovery works on: a *sqlstore.DB (tests watch its calls).
type Store interface {
	Intents(ctx context.Context) ([]sqlstore.Intent, error)
	Drop(ctx context.Context, opID string) error
	Flag(ctx context.Context, opID string) error
	CompleteSave(ctx context.Context, opID string, stat sqlstore.FileStat) error
}

// Totals counts Recover's actions, for its log record.
type Totals struct {
	Dropped, Kept, RolledForward, Flagged int
}

// Recover judges every open intent, oldest first, and leaves only the flagged ones. A
// flagged intent is left as it is: the operator decides (#199). An intent it can't end
// stops it with an error naming that intent's kind and op ID, never a path, and the start
// refuses (the runbook's "Start refuses: intent recovery").
func Recover(ctx context.Context, r Recovery) (Totals, error) {
	var t Totals
	err := r.Vault.Locked(ctx, func() error {
		in, err := r.DB.Intents(ctx)
		if err != nil {
			return err
		}
		for _, i := range in {
			if i.Flagged {
				continue
			}
			if err := r.recoverOne(ctx, i, &t); err != nil {
				return fmt.Errorf("intent recovery: the %s intent %s: %w", i.Kind, i.OpID, err)
			}
		}
		return nil
	})
	return t, err
}

func (r Recovery) recoverOne(ctx context.Context, in sqlstore.Intent, t *Totals) error {
	if noRecoveryYet(in.Kind) {
		return r.flag(ctx, in, "no recovery for this kind of intent in this build", t, "", true)
	}
	if in.Kind != sqlstore.IntentSave {
		return r.flag(ctx, in, "an unknown kind of intent", t, "", false)
	}
	var p sqlstore.SaveIntent
	if err := in.Decode(&p); err != nil {
		return r.flag(ctx, in, "the payload doesn't decode", t, "", false)
	}
	return r.recoverSave(ctx, in, p, t)
}

// noRecoveryYet is the one place for the kinds whose recovery hasn't shipped (ADR-0033): their
// intents are flagged, never guessed at. Each later PR deletes its kind here as its recovery
// lands, and the last one deletes this function; scripts/verify-release-tag.sh refuses a
// format-2 release while it exists.
func noRecoveryYet(kind string) bool {
	switch kind {
	case sqlstore.IntentCreate, sqlstore.IntentMove, sqlstore.IntentDelete, sqlstore.IntentRestore:
		return true
	}
	return false
}

// drop ends the intent as not applied: its temp removed, then the transaction (ADR-0033's
// drop). A crash between them leaves no temp, which the table drops again.
func (r Recovery) drop(ctx context.Context, opID string, t *Totals) error {
	if err := r.Vault.RemoveTemp(opID); err != nil {
		return err
	}
	if err := r.DB.Drop(ctx, opID); err != nil {
		return err
	}
	t.Dropped++
	return nil
}

// dropKeeping is drop, after a durable copy of the complete temp in recovery/<op-id>/ and a
// warning: no audit event and no flag (ADR-0033's "drop, keeping the temp").
func (r Recovery) dropKeeping(ctx context.Context, in sqlstore.Intent, t *Totals) error {
	if err := sqlstore.KeepIntentCopy(r.State, in.OpID, "temp", r.openTemp(in.OpID)); err != nil {
		return err
	}
	r.Log.Warn("a save that didn't finish was dropped and its edit kept in the state directory's recovery folder: "+
		"the note had changed outside PostelMD meanwhile (ADR-0033)",
		"op_id", in.OpID, "kind", in.Kind)
	if err := r.drop(ctx, in.OpID, t); err != nil {
		return err
	}
	t.Dropped--
	t.Kept++
	return nil
}

// flag is interference: each side that is a regular file is copied into recovery/<op-id>/
// (the temp, if temp, and the vault path target, if not ""), the intent is flagged, and an
// error and an audit event name its op ID and kind and why.
func (r Recovery) flag(ctx context.Context, in sqlstore.Intent, why string, t *Totals, target string, temp bool) error {
	if temp {
		if err := r.keepIfFile(in.OpID, "temp", func() (io.ReadCloser, error) { return r.Vault.OpenTemp(in.OpID) }); err != nil {
			return err
		}
	}
	if target != "" {
		if err := r.keepIfFile(in.OpID, "target", func() (io.ReadCloser, error) { return r.Vault.OpenNote(target) }); err != nil {
			return err
		}
	}
	if err := r.DB.Flag(ctx, in.OpID); err != nil {
		return err
	}
	r.Log.Error("an intent met a state no PostelMD operation leaves (interference): it is flagged, and what was "+
		"there is copied into the state directory's recovery folder; the paths it names stay as they are until "+
		"an operator decides (ADR-0033)", "op_id", in.OpID, "kind", in.Kind, "why", why)
	r.Audit.Record(ctx, "intent.flagged", slog.String("op_id", in.OpID), slog.String("kind", in.Kind), slog.String("why", why))
	t.Flagged++
	return nil
}

// keepIfFile copies a side that is a regular file; one that isn't (gone, a folder, a
// symlink) has nothing to copy.
func (r Recovery) keepIfFile(opID, name string, open func() (io.ReadCloser, error)) error {
	f, err := open()
	if errors.Is(err, vault.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	_ = f.Close()
	return sqlstore.KeepIntentCopy(r.State, opID, name, open)
}

func (r Recovery) openTemp(opID string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return r.Vault.OpenTemp(opID) }
}

// Remaining reports whether a temp's name is an intent's op ID: after recovery, the
// flagged intents' temps, which the sweep spares (ADR-0033).
func Remaining(ctx context.Context, db Store) (func(name string) bool, error) {
	in, err := db.Intents(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, i := range in {
		names[i.OpID] = true
	}
	return func(name string) bool { return names[name] }, nil
}

func fileStat(s fs.Stat) sqlstore.FileStat {
	return sqlstore.FileStat{Size: s.Size, Device: s.Device, Inode: s.Inode, MtimeNS: s.MtimeNS, CtimeNS: s.CtimeNS}
}
