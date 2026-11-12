//go:build linux

package auth

import (
	"bytes"
	"log/slog"
	"os"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/audit"
)

// TestFailedResetIsNotAudited: a reset whose new credential can't be saved changes nothing,
// so it records no password.reset (#144).
func TestFailedResetIsNotAudited(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes read-only directories")
	}
	e := newEnv(t)
	setUp(t, e)
	if err := os.Chmod(e.dir, 0o500); err != nil { //nolint:gosec // read-only, but still listable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.dir, 0o700) }) //nolint:gosec // removing a directory needs its x bit
	var logs bytes.Buffer
	if err := ResetPassword(ctx, e.dir, "the reset password", audit.New(slog.New(slog.NewTextHandler(&logs, nil)))); err == nil {
		t.Fatal("a reset into a read-only state directory succeeded")
	}
	if logs.Len() != 0 {
		t.Fatalf("a failed reset was audited: %s", logs.String())
	}
}
