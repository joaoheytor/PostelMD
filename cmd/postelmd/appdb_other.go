//go:build !linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// openAppDB is unsupported outside Linux, like the store it needs (internal/store/sql).
func openAppDB(_ context.Context, state, _ string, _ *slog.Logger) (appDB, error) {
	return nil, fmt.Errorf("sql: %s: %w", state, errors.ErrUnsupported)
}
