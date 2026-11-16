//go:build !linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/joaoheytor/PostelMD/internal/config"
)

// openAppDB is unsupported outside Linux, like the store it needs (internal/store/sql).
func openAppDB(_ context.Context, state, _ string, _ *slog.Logger) (appDB, error) {
	return nil, fmt.Errorf("sql: %s: %w", state, errors.ErrUnsupported)
}

// dbRollback is unsupported outside Linux, like the store it needs.
func dbRollback(_ config.Config, _ io.Writer) error {
	return fmt.Errorf("db rollback: %w", errors.ErrUnsupported)
}
