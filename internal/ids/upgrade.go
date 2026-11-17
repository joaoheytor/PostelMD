//go:build linux

package ids

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

var (
	// ErrAnotherVault: the vault marker names another vault UUID than the database's, or than
	// the one a resumed upgrade assigns. Nothing more was changed.
	ErrAnotherVault = errors.New("ids: the vault marker names another vault than this state directory's. " +
		"Mount the vault this state belongs to; or, to use this one with new file IDs, run postelmd db rebuild " +
		"with the server stopped (ADR-0030)")
	// ErrStateNotHere: at data format 1, before any upgrade, the vault marker names a vault
	// UUID: the vault belongs to a v0.2 instance whose state directory isn't this one.
	ErrStateNotHere = errors.New("ids: the vault marker names a vault UUID, so this vault belongs to a v0.2 " +
		"instance whose state directory isn't this one. Mount that state directory; after a rollback to v0.1 " +
		"that didn't empty the marker, run postelmd db rebuild with the server stopped (ADR-0030)")
)

// UpgradeOptions are the first upgrade's inputs.
type UpgradeOptions struct {
	State, Vault string // the state directory and the vault, absolute
	Format       int    // the data format written into the marker and the format file
	Now          time.Time
	FS           []fs.Option
}

// Upgraded is what Upgrade did.
type Upgraded struct {
	sqlstore.Upgrade
	Resumed  bool // an interrupted upgrade was finished
	KeptCopy bool // pre-format-2/ was complete already
	Files    int  // files given an ID (0 when a resumed run found them assigned)
	Trash    int  // trash items imported
}

// Upgrade runs the first upgrade from data format 1 (ADR-0030; intent and recovery table in
// ADR-0041) on db, the state directory's app.db open and migrated (step 2), and v, the
// vault open. With upgrade.intent durable first, it copies the v0.1 state into
// pre-format-2/, assigns every file an ID under the intent's vault UUID in one transaction,
// writes the marker and the format, and removes the intent. With the intent already there it
// finishes that upgrade; each step is safe to run again. It refuses, changing nothing more,
// a database with IDs it didn't assign and a marker naming another vault. The caller holds
// the instance lock (CLAUDE.md rule 12).
func Upgrade(ctx context.Context, db *sqlstore.DB, v *vault.Vault, o UpgradeOptions) (Upgraded, error) {
	var out Upgraded
	in, id, err := upgradeChecks(ctx, db, o)
	if err != nil {
		return out, err
	}
	out.Resumed = in != nil
	if in == nil {
		u, err := sqlstore.BeginUpgrade(o.State, o.Now)
		if err != nil {
			return out, err
		}
		in = &u
	}
	out.Upgrade = *in
	killPoint("upgrade-intent-written")
	tag := in.VaultUUID + " " + in.Time + "\n" // binds pre-format-2/ to this upgrade (ADR-0041)
	if out.KeptCopy, err = layout.CopyPreFormat2(o.State, in.Stamp(), tag, copyStep, o.FS...); err != nil {
		return out, err
	}
	if !id.Assigned {
		a, err := assignment(ctx, v, in.VaultUUID, "", o.Now)
		if err != nil {
			return out, err
		}
		if err := db.Assign(ctx, a); err != nil {
			return out, err
		}
		out.Files, out.Trash = len(a.Files), len(a.Trash)
		killPoint("upgrade-assigned")
	}
	if err := vault.WriteMarker(o.Vault, in.VaultUUID, o.Format, o.FS...); err != nil {
		return out, err
	}
	killPoint("upgrade-marker-written")
	if err := layout.SetFormat(o.State, o.Format, o.FS...); err != nil {
		return out, err
	}
	killPoint("upgrade-format-written")
	return out, sqlstore.FinishUpgrade(o.State)
}

// CheckUpgrade runs Upgrade's refusals and changes nothing: o.Vault isn't opened, so serve
// runs it before the vault opens, whose adoption, temp sweep and rename probe would change a
// vault the upgrade then refuses (ADR-0030, ADR-0042). Only o.State, o.Vault and o.FS are read.
func CheckUpgrade(ctx context.Context, db *sqlstore.DB, o UpgradeOptions) error {
	_, _, err := upgradeChecks(ctx, db, o)
	return err
}

// upgradeChecks reads the intent (nil before the upgrade starts) and the database's identity,
// and refuses a flag the upgrade didn't set, a marker naming a vault before the upgrade or
// another than the intent's, and a marker that can't be read.
func upgradeChecks(ctx context.Context, db *sqlstore.DB, o UpgradeOptions) (*sqlstore.Upgrade, sqlstore.Identity, error) {
	in, err := sqlstore.ReadUpgradeIntent(o.State)
	if err != nil {
		return nil, sqlstore.Identity{}, err
	}
	id, err := db.Identity(ctx)
	if err != nil {
		return nil, id, err
	}
	want := ""
	if in != nil {
		want = in.VaultUUID
	}
	if id.Assigned && id.VaultUUID != want {
		return nil, id, fmt.Errorf("%w: the database has file IDs this upgrade didn't assign", sqlstore.ErrUpgradeInterference)
	}
	m, err := vault.ReadMarker(o.Vault, o.FS...)
	switch {
	case err != nil:
		return nil, id, err
	case m.VaultUUID != "" && in == nil:
		return nil, id, ErrStateNotHere
	case m.VaultUUID != "" && m.VaultUUID != want:
		return nil, id, ErrAnotherVault
	}
	return in, id, nil
}

// copyStep turns pre-format-2's steps into kill points.
func copyStep(s string) {
	switch s {
	case "moved-aside":
		killPoint("upgrade-copy-moved-aside")
	case "copied":
		killPoint("upgrade-copied")
	case "complete":
		killPoint("upgrade-copy-complete")
	}
}
