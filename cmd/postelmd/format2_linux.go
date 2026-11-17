//go:build linux

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/joaoheytor/PostelMD/internal/ids"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// checkDataFormat is ADR-0030's step 2: the data format, under the instance lock. At build
// format 1 a missing format file is written, as always. From build format 2 it is read as
// format 1 and nothing is written (ADR-0042), and the first upgrade runs when the data is at
// format 1 or upgrade.intent is there (an upgrade killed after writing the format, ADR-0041).
func checkDataFormat(state string) (dataStart, error) {
	format, err := layout.CheckFormat(state, buildFormat)
	if err != nil || buildFormat < 2 {
		return dataStart{format: format}, err
	}
	in, err := sqlstore.ReadUpgradeIntent(state)
	if err != nil {
		return dataStart{}, err
	}
	return dataStart{format: format, upgrade: format < 2 || in != nil}, nil
}

// checkVaultAtStart runs, from build format 2, ADR-0030's format table for data at format 2
// with no upgrade to finish (ids.CheckVault), before the vault opens. The format-1 rows are
// the first upgrade's own refusals (ADR-0041).
func checkVaultAtStart(ctx context.Context, db appDB, start dataStart, vaultDir string, log *slog.Logger) error {
	sdb, ok := db.(*sqlstore.DB)
	if buildFormat < 2 || start.upgrade || !ok {
		return nil
	}
	return ids.CheckVault(ctx, sdb, vaultDir, buildFormat, log)
}

// firstUpgrade runs or resumes the first upgrade (ADR-0030, ADR-0041) when the format check
// said so, on the open database and vault, and logs what it did.
func firstUpgrade(ctx context.Context, db appDB, v *vault.Vault, start dataStart, paths layout.Paths, log *slog.Logger) error {
	sdb, ok := db.(*sqlstore.DB)
	if !start.upgrade || !ok {
		return nil
	}
	t := time.Now()
	u, err := ids.Upgrade(ctx, sdb, v, ids.UpgradeOptions{State: paths.State, Vault: paths.Vault, Format: buildFormat, Now: t})
	if err != nil {
		return err
	}
	log.Info("first upgrade", "vault_uuid", u.VaultUUID, "resumed", u.Resumed, "kept_copy", u.KeptCopy,
		"files", u.Files, "trash", u.Trash, "format", buildFormat, "duration_ms", time.Since(t).Milliseconds())
	return nil
}
