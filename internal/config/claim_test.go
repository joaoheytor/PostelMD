package config

import (
	"errors"
	"io"
	"testing"
)

// TestClaimFlags: --yes and --force are vault claim's only; another command refuses them.
func TestClaimFlags(t *testing.T) {
	none := func(string) string { return "" }
	c, err := load("vault claim", []string{"--data", t.TempDir(), "--yes", "--force"}, none, io.Discard)
	if err != nil || !c.Yes || !c.Force {
		t.Fatalf("vault claim --yes --force = %+v, %v", c, err)
	}
	for _, name := range []string{"serve", "db rebuild"} {
		if _, err := load(name, []string{"--yes"}, none, io.Discard); !errors.Is(err, ErrPrinted) {
			t.Fatalf("%s --yes = %v, want a flag error", name, err)
		}
	}
}
