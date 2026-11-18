//go:build linux

package intents

import (
	"bytes"
	"context"
	"errors"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// recoverSave is ADR-0033's save table: the target T and the temp P, each judged on its
// hash and inode, never on one signal alone.
func (r Recovery) recoverSave(ctx context.Context, in sqlstore.Intent, p sqlstore.SaveIntent, t *Totals) error {
	path := string(p.Path)
	T, err := r.Vault.Judge(ctx, path)
	if errors.Is(err, vault.ErrNotFound) { // a reserved or invalid path: the store never writes one
		return r.flag(ctx, in, "the intent names a path no save writes", t, "", true)
	}
	if err != nil {
		return err
	}
	P, err := r.Vault.JudgeTemp(ctx, in.OpID)
	if errors.Is(err, vault.ErrNotFound) {
		return r.flag(ctx, in, "the op ID isn't a temp's name", t, "", false)
	}
	if err != nil {
		return err
	}
	is := func(j vault.Judged, sum []byte) bool { return j.Regular && bytes.Equal(j.SHA256[:], sum) }
	sameInode := sameFile(T, P)
	switch {
	case P.Present && !P.Regular:
		return r.flag(ctx, in, "the temp isn't a regular file", t, path, false)
	case sameInode && is(T, p.NewSHA256):
		// A power loss kept the temp's name for the renamed file: unlink it, then as applied.
		if err := r.Vault.RemoveTemp(in.OpID); err != nil {
			return err
		}
		return r.forward(ctx, in, path, t)
	case sameInode:
		return r.flag(ctx, in, "the temp is the target's file, with other bytes", t, path, true)
	case is(T, p.BaseSHA256):
		return r.drop(ctx, in.OpID, t)
	case is(T, p.NewSHA256) && !P.Present:
		return r.forward(ctx, in, path, t)
	case is(T, p.NewSHA256):
		return r.drop(ctx, in.OpID, t) // another writer put the same bytes there; the scan records it
	case is(P, p.NewSHA256):
		return r.dropKeeping(ctx, in, t) // the 412-type race, with the edit complete in the temp
	default:
		return r.drop(ctx, in.OpID, t) // the 412-type race; the client still has its draft
	}
}

// sameFile reports whether two regular files are one: the same device and inode.
func sameFile(a, b vault.Judged) bool {
	return a.Regular && b.Regular && a.Stat.Device == b.Stat.Device && a.Stat.Inode == b.Stat.Inode
}

// forward rolls a save forward: the rename made durable (the target's folder, then the temp
// folder), then the completion with the target's stat now. A row that isn't at the intent's
// base (ErrIntentStale) is flagged instead.
func (r Recovery) forward(ctx context.Context, in sqlstore.Intent, path string, t *Totals) error {
	if err := r.Vault.SyncSaved(path); err != nil {
		return err
	}
	st, ok, err := r.Vault.StatPath(path)
	if err != nil {
		return err
	}
	if !ok {
		return r.flag(ctx, in, "the target went while it was rolled forward", t, "", false)
	}
	err = r.DB.CompleteSave(ctx, in.OpID, fileStat(st))
	if errors.Is(err, sqlstore.ErrIntentStale) {
		return r.flag(ctx, in, "the file's row isn't at the intent's base", t, path, false)
	}
	if err != nil {
		return err
	}
	t.RolledForward++
	return nil
}
