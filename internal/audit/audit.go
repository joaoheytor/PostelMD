// Package audit records security-relevant events (CLAUDE.md rule 15). In the MVP there is no
// database, so events go to the external sink only: structured slog records on stdout
// (ADR-0020). Never pass note content, secrets, tokens or passwords.
package audit

import (
	"context"
	"log/slog"
)

// Log writes audit events.
type Log struct{ l *slog.Logger }

// New returns an audit log writing to l.
func New(l *slog.Logger) *Log { return &Log{l: l} }

// Record writes one event, for example "login.failed", with its attributes.
func (a *Log) Record(ctx context.Context, event string, attrs ...slog.Attr) {
	a.l.LogAttrs(ctx, slog.LevelInfo, "audit", append([]slog.Attr{slog.String("event", event)}, attrs...)...)
}
