//go:build linux

package layout

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepare(t *testing.T) {
	data := t.TempDir()
	p, _, err := Prepare(data, false)
	if err != nil {
		t.Skipf("the test's temp filesystem isn't on the allow-list: %v", err)
	}
	for _, d := range []string{p.State, p.Vault} {
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", d, st, err)
		}
	}
	if _, _, err := Prepare(data, false); err != nil {
		t.Fatalf("Prepare is not idempotent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(data, "runbook.MD"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Prepare(data, false); !errors.Is(err, ErrNotesAtRoot) {
		t.Fatalf("notes at the root = %v, want ErrNotesAtRoot", err)
	}
	if _, _, err := Prepare(filepath.Join(data, "missing"), false); err == nil {
		t.Fatal("a missing data directory was accepted")
	}
}

func TestStateFilesystemDecisions(t *testing.T) {
	// /dev/shm is tmpfs on Linux: it starts, flagged as not persistent.
	if d, err := checkStateFS("/dev/shm", false); err != nil || d != Ephemeral {
		t.Fatalf("tmpfs = %v, %v; want Ephemeral", d, err)
	}
	// /proc's magic (0x9FA0) is on no list: refused unless overridden.
	if _, err := checkStateFS("/proc", false); err == nil {
		t.Fatal("an unknown filesystem was accepted")
	}
	if _, err := checkStateFS("/proc", true); err != nil {
		t.Fatalf("the override didn't apply: %v", err)
	}
}
