//go:build linux

package ids

import (
	"context"
	"errors"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// ErrNoIDs: vault claim found a database without the "assigned" flag. Nothing was changed.
var ErrNoIDs = errors.New("ids: this database has no file IDs yet, so there is nothing to claim: " +
	"the first start after the upgrade writes the vault marker (ADR-0030)")

// ClaimCheck is what vault claim found, before it asks (ADR-0030, ADR-0040).
type ClaimCheck struct {
	Identity
	VaultUUID  string // the stored vault UUID, which the marker gets
	MarkerUUID string // the vault UUID the marker names now, when it is another one
	Done       bool   // the marker names it already: nothing to do
}

// CheckClaim reads, changing nothing, whether vault claim may write the marker of the vault
// at dir for db, and runs the identity check. A database without file IDs is ErrNoIDs; a
// marker that is a symlink, a folder or a special file is vault.ErrMarkerNotAFile; a marker
// with the wrong content is what claim rewrites.
func CheckClaim(ctx context.Context, db *sqlstore.DB, dir string, opts ...fs.Option) (ClaimCheck, error) {
	id, err := db.Identity(ctx)
	if err != nil {
		return ClaimCheck{}, err
	}
	if !id.Assigned {
		return ClaimCheck{}, ErrNoIDs
	}
	c := ClaimCheck{VaultUUID: id.VaultUUID}
	m, err := vault.ReadMarker(dir, opts...)
	switch {
	case errors.Is(err, vault.ErrMarkerNotAFile), err != nil && !errors.Is(err, vault.ErrBadMarker):
		return ClaimCheck{}, err
	case err == nil && m.VaultUUID == id.VaultUUID:
		c.Done = true
		return c, nil
	case err == nil:
		c.MarkerUUID = m.VaultUUID
	}
	c.Identity, err = CheckIdentity(ctx, db, dir, opts...)
	return c, err
}

// Claim writes the marker of the vault at dir with c's vault UUID and format.
func Claim(dir string, c ClaimCheck, format int, opts ...fs.Option) error {
	return vault.WriteMarker(dir, c.VaultUUID, format, opts...)
}
