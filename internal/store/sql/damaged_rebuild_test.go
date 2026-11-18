//go:build linux

package sql

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// TestErrDamagedNamesDBRebuild: a damaged database's refusal, as Open and OpenExisting
// return it to any caller, names postelmd db rebuild, never format 1's manual move, which
// would lose the file IDs (ADR-0034, ADR-0040); errors.Is finds ErrDamaged, and SQLite's own
// error stays in the text for the log.
func TestErrDamagedNamesDBRebuild(t *testing.T) {
	for name, open := range map[string]func(context.Context, string, *slog.Logger) (*DB, error){
		"Open": Open, "OpenExisting": OpenExisting,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, AppDB), bytes.Repeat([]byte("not a database\n"), 300))
			_, err := open(context.Background(), dir, slog.New(slog.DiscardHandler))
			if !errors.Is(err, ErrDamaged) {
				t.Fatalf("%s = %v, want ErrDamaged", name, err)
			}
			msg := err.Error()
			for _, want := range []string{"postelmd db rebuild", "damaged-", "not a database (26)"} {
				if !strings.Contains(msg, want) {
					t.Errorf("the refusal doesn't say %q: %v", want, msg)
				}
			}
			for _, unwanted := range []string{"into a new folder", "only the boot UUID", "nothing is lost"} {
				if strings.Contains(msg, unwanted) {
					t.Errorf("the refusal still gives format 1's manual fix (%q): %v", unwanted, msg)
				}
			}
		})
	}
}
