//go:build linux

package sql

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCanceledOpenIsNotAnError: an open canceled by a clean stop (serve stopped during
// startup) returns context.Canceled and logs nothing at ERROR, whichever step it hits: the
// probe before the writer opens, or quick_check on the writer.
func TestCanceledOpenIsNotAnError(t *testing.T) {
	for _, at := range []string{"before-open", "opened"} {
		t.Run(at, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if at == "before-open" {
				cancel()
			} else {
				setKillPoint(t, func(p string) {
					if p == at {
						cancel()
					}
				})
			}
			var out bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&out, nil))
			db, err := Open(ctx, dir, log)
			if err == nil {
				_ = db.Close()
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Open = %v, want context.Canceled", err)
			}
			if strings.Contains(out.String(), `"level":"ERROR"`) {
				t.Fatalf("a canceled open logged an error:\n%s", out.String())
			}
		})
	}
}
