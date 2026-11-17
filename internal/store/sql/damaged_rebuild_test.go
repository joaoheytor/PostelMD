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

// TestNameRebuild: from data format 2, a damaged database's refusal names postelmd db rebuild
// instead of format 1's manual move (ADR-0034), keeps ErrDamaged and SQLite's own error for
// errors.Is and the log, and leaves every other error as it is.
func TestNameRebuild(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, AppDB), bytes.Repeat([]byte("not a database\n"), 300))
	_, err := Open(context.Background(), dir, slog.New(slog.DiscardHandler))
	if !errors.Is(err, ErrDamaged) {
		t.Fatalf("Open = %v, want ErrDamaged", err)
	}
	got := NameRebuild(err)
	if !errors.Is(got, ErrDamaged) {
		t.Fatalf("NameRebuild lost ErrDamaged: %v", got)
	}
	msg := got.Error()
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
	plain := errors.New("disk I/O error")
	if got := NameRebuild(plain); got.Error() != plain.Error() || !errors.Is(got, plain) || NameRebuild(nil) != nil {
		t.Fatal("NameRebuild changed an error that isn't ErrDamaged")
	}
}
