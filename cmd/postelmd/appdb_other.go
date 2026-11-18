//go:build !linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/joaoheytor/PostelMD/internal/config"
	pmhttp "github.com/joaoheytor/PostelMD/internal/http"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// openAppDB is unsupported outside Linux, like the store it needs (internal/store/sql).
func openAppDB(_ context.Context, state, _ string, _ dataStart, _ *slog.Logger) (appDB, error) {
	return nil, fmt.Errorf("sql: %s: %w", state, errors.ErrUnsupported)
}

// checkDataFormat is unsupported outside Linux, like the layout it reads.
func checkDataFormat(state string) (dataStart, error) {
	_, err := layout.CheckFormat(state, buildFormat)
	return dataStart{}, err
}

// checkVaultAtStart has nothing to check outside Linux: openAppDB fails first.
func checkVaultAtStart(context.Context, appDB, dataStart, layout.Paths, *slog.Logger) error {
	return nil
}

// firstUpgrade has nothing to run outside Linux: openAppDB fails first.
func firstUpgrade(context.Context, appDB, *vault.Vault, dataStart, layout.Paths, *slog.Logger) error {
	return nil
}

// recoverIntents has no intents to recover outside Linux (openAppDB fails first); it sweeps.
func recoverIntents(_ context.Context, _ appDB, v *vault.Vault, _ string, _ *slog.Logger) error {
	return v.Sweep(nil)
}

// startIDs has nothing to scan outside Linux: openAppDB fails first.
func startIDs(context.Context, appDB, *vault.Vault, string, *slog.Logger) (pmhttp.IDs, func(), error) {
	return nil, func() {}, nil
}

// dbRollback is unsupported outside Linux, like the store it needs.
func dbRollback(_ config.Config, _ io.Writer) error {
	return fmt.Errorf("db rollback: %w", errors.ErrUnsupported)
}

// dbRebuild is unsupported outside Linux, like the store it needs.
func dbRebuild(_ config.Config, _ io.Writer) error {
	return fmt.Errorf("db rebuild: %w", errors.ErrUnsupported)
}

// vaultClaim is unsupported outside Linux, like the store it needs.
func vaultClaim(_ config.Config, _ *os.File, _ io.Writer) error {
	return fmt.Errorf("vault claim: %w", errors.ErrUnsupported)
}
