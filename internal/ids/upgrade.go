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

// ErrAnotherVault: the vault marker names another vault UUID than the one this upgrade
// assigns. Nothing more was changed.
var ErrAnotherVault = errors.New("ids: the vault marker names another vault than this state directory's; " +
	"mount the vault this state belongs to (ADR-0030)")

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
	in, err := sqlstore.ReadUpgradeIntent(o.State)
	if err != nil {
		return out, err
	}
	id, err := db.Identity(ctx)
	if err != nil {
		return out, err
	}
	want := ""
	if in != nil {
		want = in.VaultUUID
	}
	if id.Assigned && id.VaultUUID != want {
		return out, fmt.Errorf("%w: the database has file IDs this upgrade didn't assign", sqlstore.ErrUpgradeInterference)
	}
	m, err := vault.ReadMarker(o.Vault, o.FS...)
	if err != nil {
		return out, err
	}
	if m.VaultUUID != "" && m.VaultUUID != want {
		return out, ErrAnotherVault
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
