//go:build linux

package ids

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// RebuildOptions are db rebuild's inputs.
type RebuildOptions struct {
	State, Vault string // the state directory and the vault, absolute
	Format       int    // the data format written into the marker and the format file
	Now          time.Time
	Log          *slog.Logger // the temp-folder entries not copied, by name and kind (never content)
	FS           []fs.Option
}

// Rebuilt is what Rebuild did.
type Rebuilt struct {
	sqlstore.Rebuild
	Resumed   bool   // an interrupted rebuild was finished
	VaultUUID string // the new vault UUID, in the database and the marker
	Kept      int    // temps kept in the recovery folder by this run
	Skipped   int    // temp-folder entries this run didn't copy (not regular files)
	Files     int    // files given an ID (0 when a resumed run found them assigned)
	Trash     int    // trash items imported
}

// Rebuild runs postelmd db rebuild (ADR-0030, mechanics and recovery table in ADR-0040): with
// rebuild.intent durable first, it moves the database aside, keeps the vault's temps in
// recovery/, creates and migrates a new database, gives every file a new ID under a new vault
// UUID in one transaction, writes the marker and the format, and removes the intent. With an
// intent already there, it finishes that rebuild from the step its files show. The caller
// holds the instance lock and has passed the data format check (CLAUDE.md rule 12).
func Rebuild(ctx context.Context, o RebuildOptions) (Rebuilt, error) {
	var out Rebuilt
	r, err := sqlstore.ReadRebuildIntent(o.State)
	if err != nil {
		return out, err
	}
	out.Resumed = r != nil
	if r == nil {
		// Refused before the first side effect, like the state directory's own refusals: the
		// marker, and what step 4's vault.Open would refuse on.
		_, err := vault.ReadMarker(o.Vault, o.FS...)
		if errors.Is(err, vault.ErrMarkerNotAFile) || err != nil && !errors.Is(err, vault.ErrBadMarker) {
			return out, fmt.Errorf("%w: %w", sqlstore.ErrRebuildRefused, err)
		}
		if err := vault.CheckOpenable(o.Vault, o.FS...); err != nil {
			return out, fmt.Errorf("%w: %w", sqlstore.ErrRebuildRefused, err)
		}
		b, err := sqlstore.BeginRebuild(ctx, o.State, o.Now)
		if err != nil {
			return out, err
		}
		r = &b
	}
	out.Rebuild = *r
	killPoint("rebuild-intent-written")
	built, err := sqlstore.MoveAsideForRebuild(ctx, o.State, *r)
	if err != nil {
		return out, err
	}
	killPoint("rebuild-moved-synced")
	if out.Kept, err = vault.TakeTemps(o.Vault, temps(o, *r, &out), o.FS...); err != nil {
		return out, err
	}
	db, err := sqlstore.OpenForRebuild(ctx, o.State, o.Log)
	if err != nil {
		return out, err
	}
	err = assignAndMark(ctx, db, o, *r, built, &out)
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return out, err
	}
	if err := layout.SetFormat(o.State, o.Format, o.FS...); err != nil {
		return out, err
	}
	killPoint("rebuild-format-written")
	return out, sqlstore.FinishRebuild(o.State)
}

// temps is step 3's handler: each temp is kept in r's recovery folder; an entry left in
// place is logged by name and kind, as attributes (#109).
func temps(o RebuildOptions, r sqlstore.Rebuild, out *Rebuilt) vault.TempHandler {
	return vault.TempHandler{
		Keep: func(name string, src io.Reader) error {
			if err := sqlstore.KeepRecovered(o.State, r, name, src); err != nil {
				return err
			}
			killPoint("rebuild-temp-kept")
			return nil
		},
		Removed: func(string) { killPoint("rebuild-temp-removed") },
		Skipped: func(name, kind string) {
			out.Skipped++
			o.Log.Warn("db rebuild didn't copy an entry of the vault's temp folder; the vault's start-up sweep removes it", "name", name, "kind", kind)
		},
	}
}

// assignAndMark is step 4, unless a run before a crash committed it (built), then the marker.
func assignAndMark(ctx context.Context, db *sqlstore.DB, o RebuildOptions, r sqlstore.Rebuild, built bool, out *Rebuilt) error {
	killPoint("rebuild-db-created")
	if !built {
		if err := assign(ctx, db, o, r, out); err != nil {
			return err
		}
		killPoint("rebuild-assigned")
	}
	id, err := db.Identity(ctx)
	if err != nil {
		return err
	}
	if id.RebuildUUID != r.UUID {
		return fmt.Errorf("%w: the new database doesn't carry the rebuild's UUID", sqlstore.ErrRebuildInterference)
	}
	out.VaultUUID = id.VaultUUID
	if err := vault.WriteMarker(o.Vault, id.VaultUUID, o.Format, o.FS...); err != nil {
		return err
	}
	killPoint("rebuild-marker-written")
	return nil
}

// assign is the first assignment's transaction (ADR-0031) with a new vault UUID and the
// rebuild's UUID. It opens the vault, which adopts it and sweeps what step 3 left.
func assign(ctx context.Context, db *sqlstore.DB, o RebuildOptions, r sqlstore.Rebuild, out *Rebuilt) error {
	v, err := vault.Open(o.Vault, o.FS...)
	if err != nil {
		return err
	}
	defer func() { _ = v.Close() }()
	a, err := assignment(ctx, v, sqlstore.NewUUID(), r.UUID, o.Now)
	if err != nil {
		return err
	}
	out.Files, out.Trash = len(a.Files), len(a.Trash)
	return db.Assign(ctx, a)
}

// assignment is the first assignment's content (ADR-0031): every file of v at generation 1
// and its trash items, under vaultUUID (and rebuildUUID, for db rebuild).
func assignment(ctx context.Context, v *vault.Vault, vaultUUID, rebuildUUID string, now time.Time) (sqlstore.Assignment, error) {
	obs, err := v.Survey(ctx)
	if err != nil {
		return sqlstore.Assignment{}, err
	}
	trash, err := v.TrashForImport()
	if err != nil {
		return sqlstore.Assignment{}, err
	}
	a := sqlstore.Assignment{VaultUUID: vaultUUID, RebuildUUID: rebuildUUID, Now: now}
	for _, f := range obs {
		nf := sqlstore.NewFile{Path: f.Path, Stat: fileStat(f.Stat)}
		if f.SHA256 != nil {
			nf.SHA256 = f.SHA256[:]
		}
		a.Files = append(a.Files, nf)
	}
	for _, t := range trash {
		a.Trash = append(a.Trash, sqlstore.TrashImport{OpID: t.ID, Name: t.Name, Kind: string(t.Kind), DeletedAt: t.DeletedAt})
	}
	return a, nil
}
