//go:build linux

package sql

import (
	"context"
	"testing"
)

// TestBootUUID: SetBootUUID records this process's boot UUID in meta (ADR-0014), replacing
// the last start's; BootUUID reads the committed value from the read pool, so a write by
// another handle on the same file (a second writer that slipped past the instance lock) is
// seen.
func TestBootUUID(t *testing.T) {
	ctx := context.Background()
	db, dir := newDB(t)
	if got, err := db.BootUUID(ctx); err != nil || got != "" {
		t.Fatalf("before any start: %q, %v; want empty", got, err)
	}
	for _, id := range []string{"first-boot", "second-boot"} {
		if err := db.SetBootUUID(ctx, id); err != nil {
			t.Fatal(err)
		}
		if got, err := db.BootUUID(ctx); err != nil || got != id {
			t.Fatalf("BootUUID = %q, %v; want %q", got, err, id)
		}
	}
	other, err := Open(ctx, dir, discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetBootUUID(ctx, "intruder"); err != nil {
		t.Fatal(err)
	}
	_ = other.Close()
	if got, err := db.BootUUID(ctx); err != nil || got != "intruder" {
		t.Fatalf("after another handle wrote: %q, %v; want intruder", got, err)
	}
	var n int
	if err := db.r.QueryRow("SELECT count(*) FROM meta WHERE key = 'boot_uuid'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("boot_uuid rows = %d, %v; want 1", n, err)
	}
}
