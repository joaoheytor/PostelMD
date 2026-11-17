//go:build linux

package ids

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

var (
	// ErrNoIDsAtFormat2: the data is at format 2, but app.db has no file IDs, so it isn't the
	// database the first upgrade left. Nothing was changed.
	ErrNoIDsAtFormat2 = errors.New("ids: the data is at format 2, but app.db has no file IDs, so it isn't " +
		"this state directory's database; with the server stopped, postelmd db rebuild makes a new one with " +
		"new file IDs (ADR-0030, ADR-0042)")
	// ErrVaultNotClaimed: the vault marker is missing or empty, and the identity check can't
	// say the mounted vault is the database's. Nothing was changed.
	ErrVaultNotClaimed = errors.New("ids: the mounted vault has no vault marker naming this database's vault, " +
		"and its files don't match the database's. Mount the folder this state belongs to; if this is that " +
		"vault, run postelmd vault claim with the server stopped (ADR-0030)")
)

// CheckVault is ADR-0030's format table at data format 2, run at start before the vault
// opens (opening adopts the folder, which would write the empty marker the table must tell
// from a missing one). The marker naming the database's vault UUID goes on. A database
// without the "assigned" flag, a marker naming another UUID or that can't be read, and a
// missing or empty marker the identity check can't vouch for are refused, changing
// nothing. A missing marker with no live rows, or an empty one the identity check passes, is
// rewritten with the stored UUID and format, with a warning. The caller holds the instance
// lock (CLAUDE.md rule 12).
func CheckVault(ctx context.Context, db *sqlstore.DB, dir string, format int, log *slog.Logger, opts ...fs.Option) error {
	id, err := db.Identity(ctx)
	if err != nil {
		return err
	}
	if !id.Assigned {
		return ErrNoIDsAtFormat2
	}
	m, err := vault.ReadMarker(dir, opts...)
	switch {
	case errors.Is(err, vault.ErrBadMarker):
		return fmt.Errorf("%w. If this is the vault this state belongs to, postelmd vault claim rewrites "+
			"the marker with the server stopped (ADR-0030)", err)
	case err != nil:
		return err
	case m.VaultUUID == id.VaultUUID:
		return nil
	case m.VaultUUID != "":
		return ErrAnotherVault
	}
	c, err := CheckIdentity(ctx, db, dir, opts...)
	if err != nil {
		return err
	}
	if m.Present && !c.Passes() || !m.Present && c.Sampled > 0 {
		return fmt.Errorf("%w; %d of %d sampled rows match", ErrVaultNotClaimed, c.Matched, c.Sampled)
	}
	if err := vault.WriteMarker(dir, id.VaultUUID, format, opts...); err != nil {
		return err
	}
	log.Warn("rewrote the vault marker with this database's vault UUID: it was missing with no live file IDs, "+
		"or empty with the files matching (ADR-0030)", "was_empty", m.Present, "sampled", c.Sampled,
		"matched", c.Matched)
	return nil
}
