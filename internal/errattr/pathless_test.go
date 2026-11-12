package errattr

import (
	"bytes"
	"errors"
	"fmt"
	iofs "io/fs"
	"log/slog"
	"strings"
	"syscall"
	"testing"
)

// TestPathlessLogsNoUserText: an unexpected error is logged as values that can't carry user
// text (CLAUDE.md rule 15): a PathError's path, an inner error quoting a path, and a line
// break meant to forge a log line never reach the log, and an unknown Op is not echoed.
func TestPathlessLogsNoUserText(t *testing.T) {
	secret := "incident-" + strings.Repeat("q7", 4) + ".md"
	for _, tc := range []struct {
		name string
		err  error
		want string // substring of the JSON line
	}{
		{"errno inside", fmt.Errorf("vault: %w", &iofs.PathError{Op: "rename", Path: ".postelmd-trash/" + secret, Err: syscall.EIO}),
			`"err":{"op":"rename","errno":"EIO","kind":"unexpected","type":"syscall.Errno"}`},
		{"inner error quoting a path", &iofs.PathError{Op: "open", Path: secret,
			Err: errors.New("cannot open " + secret + "\n" + `{"level":"INFO","msg":"forged"}`)},
			`"err":{"op":"open","errno":"none","kind":"unexpected","type":"*errors.errorString"}`},
		{"user text as the Op", &iofs.PathError{Op: secret + "\nforged", Path: "x", Err: iofs.ErrNotExist},
			`"err":{"op":"unknown","errno":"none","kind":"not exist","type":"*errors.errorString"}`},
		{"plain error", errors.New(secret), `"err":{"op":"unknown","errno":"none","kind":"unexpected","type":"*errors.errorString"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			slog.New(slog.NewJSONHandler(&buf, nil)).Error("trash API", Pathless(tc.err))
			line := buf.String()
			if strings.Contains(line, secret) || strings.Contains(line, "forged") || strings.Count(line, "\n") != 1 {
				t.Fatalf("the log leaks user text: %s", line)
			}
			if !strings.Contains(line, tc.want) {
				t.Fatalf("logged %s, want %s", line, tc.want)
			}
		})
	}
}
