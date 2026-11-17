//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
)

// StuckUpgrade tells why the first upgrade under upgrade.intent can't finish, so that db
// rebuild may take it over (ADR-0041): "" when there is no intent, or the next start can
// finish it. The reasons are positive findings only: an intent PostelMD didn't write, an
// app.db with file IDs under another vault UUID than the intent's, a damaged app.db, or a
// vault marker naming another vault or not parsing (otherVault, which internal/ids answers
// from the vault). Anything it can't read (anything but a regular file at the intent's name,
// an I/O or permission error) is an error, never a reason. It changes nothing.
func StuckUpgrade(ctx context.Context, stateDir string, otherVault func(vaultUUID string) (bool, error)) (string, error) {
	if ok, err := regularFile(filepath.Join(stateDir, UpgradeIntent)); err != nil {
		return "", fmt.Errorf("sql: upgrade.intent isn't a regular file; move it aside: %w", err)
	} else if !ok {
		return "", nil
	}
	in, err := ReadUpgradeIntent(stateDir)
	switch {
	case errors.Is(err, ErrUpgradeInterference):
		return "upgrade.intent isn't one PostelMD wrote", nil
	case err != nil:
		return "", err
	}
	if reason, err := stuckDB(ctx, filepath.Join(stateDir, AppDB), in.VaultUUID); reason != "" || err != nil {
		return reason, err
	}
	other, err := otherVault(in.VaultUUID)
	if err != nil {
		return "", err
	}
	if other {
		return "the vault marker names another vault than the upgrade's, or doesn't parse", nil
	}
	return "", nil
}

// stuckDB reads app.db at path without changing it: "damaged" (SQLITE_CORRUPT or
// SQLITE_NOTADB), file IDs under another vault UUID than vaultUUID, or "" (no app.db, no
// IDs, or the intent's).
func stuckDB(ctx context.Context, path, vaultUUID string) (string, error) {
	if ok, err := regularFile(path); err != nil || !ok {
		return "", err
	}
	q := copyQuery
	if wal, err := regularFile(path + "-wal"); err != nil || !wal {
		q += "&immutable=1" // no WAL: read the file alone, creating no -wal or -shm
	}
	const isDamaged = "app.db is damaged"
	p, err := openPool(path, q, 1)
	if errors.Is(damaged(err), ErrDamaged) {
		return isDamaged, nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = p.Close() }()
	var tables int
	err = p.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'").Scan(&tables)
	if errors.Is(damaged(err), ErrDamaged) {
		return isDamaged, nil
	}
	if err != nil || tables == 0 {
		return "", err
	}
	var assigned int
	var v string
	err = p.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM meta WHERE key = ?), coalesce((SELECT value FROM meta WHERE key = ?), '')",
		assignedKey, vaultUUIDKey).Scan(&assigned, &v)
	switch {
	case errors.Is(damaged(err), ErrDamaged):
		return isDamaged, nil
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", err
	case assigned > 0 && v != vaultUUID:
		return "app.db has file IDs under another vault UUID than the upgrade's", nil
	}
	return "", nil
}
