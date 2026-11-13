//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// bootKey is the meta row holding the boot UUID of the process that last started (ADR-0014).
const bootKey = "boot_uuid"

// SetBootUUID records id as the boot UUID of this start, replacing the last start's.
func (db *DB) SetBootUUID(ctx context.Context, id string) error {
	return db.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", bootKey, id)
		if err != nil {
			return fmt.Errorf("sql: record the boot UUID: %w", err)
		}
		return nil
	})
}

// BootUUID reads the recorded boot UUID from the read pool (the last committed value, so a
// write by another process on the same file is seen); "" when none is recorded.
func (db *DB) BootUUID(ctx context.Context) (string, error) {
	var id string
	err := db.r.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", bootKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("sql: read the boot UUID: %w", err)
	}
	return id, nil
}
