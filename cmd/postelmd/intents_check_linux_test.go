//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/intents"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// TestIntentsResolveChecksTheVault: resolve runs the vault check a start runs before any vault
// I/O (ADR-0044). On a vault with no marker, one whose marker names another vault, or another
// folder mounted in its place, it refuses, naming the fix, and changes nothing: the vault's
// tree and marker are as they were, and the intent and its temp stay.
func TestIntentsResolveChecksTheVault(t *testing.T) {
	const other = "00000000-0000-4000-8000-000000000000"
	for _, c := range []struct {
		name   string
		break_ func(t *testing.T, data string)
		want   string
	}{
		{"no marker", func(t *testing.T, data string) { must(t, os.Remove(markerOf(data))) }, "postelmd vault claim"},
		{"another vault's marker", func(t *testing.T, data string) {
			must(t, vault.WriteMarker(vaultOf(data), other, 2))
		}, "names another vault"},
		{"another folder", func(t *testing.T, data string) {
			must(t, os.Rename(vaultOf(data), filepath.Join(data, "aside")))
			must(t, os.MkdirAll(vaultOf(data), 0o700))
			must(t, os.WriteFile(filepath.Join(vaultOf(data), "a.md"), []byte("someone else's note"), 0o600))
		}, "postelmd vault claim"},
	} {
		t.Run(c.name, func(t *testing.T) {
			data, temp := flaggedData(t)
			c.break_(t, data)
			before := tree(t, filepath.Join(data, "vaults"))
			marker, _ := os.ReadFile(markerOf(data)) //nolint:gosec // G304: the test's own data
			out, errs, code := intentsCmd(data, "intents", "resolve", intentA)
			if code != 1 || strings.Contains(out, "intent.resolv") || !strings.Contains(errs, c.want) {
				t.Fatalf("resolve = %d:\n%s%s\nwant a refusal naming %q", code, out, errs, c.want)
			}
			after, _ := os.ReadFile(markerOf(data)) //nolint:gosec // G304: the test's own data
			if got := tree(t, filepath.Join(data, "vaults")); got != before || string(after) != string(marker) {
				t.Fatalf("the refused resolve changed the vault:\n%s\nwas\n%s", got, before)
			}
			if c.name == "another folder" {
				temp = filepath.Join(data, "aside", vault.TmpDir, intentA)
			}
			stillFlagged(t, data, temp)
		})
	}
}

// TestListLine: an op ID that fails its kind's format check, as one with a control character
// does, prints as <invalid op ID>, and a kind with one is quoted, so app.db can't drive the
// terminal.
func TestListLine(t *testing.T) {
	for _, c := range []struct {
		l    intents.Listed
		want string
	}{
		{intents.Listed{OpID: intentA, Kind: "save", State: "flagged"}, intentA + "  save  flagged  0001-01-01T00:00:00Z  -"},
		{intents.Listed{OpID: "x\x1b]0;owned\x07", Kind: "delete", State: "open", Copies: "/s/recovery/x"},
			"<invalid op ID>  delete  open  0001-01-01T00:00:00Z  /s/recovery/x"},
		{intents.Listed{OpID: intentA[:8] + "\x1b[2J", Kind: "save", State: "open"}, "<invalid op ID>  save  open"},
		{intents.Listed{OpID: intentA, Kind: "tag\x1b[2J", State: "flagged"}, intentA + `  "tag\x1b[2J"  flagged`},
	} {
		if got := listLine(c.l); !strings.HasPrefix(got, c.want) || strings.ContainsRune(got, '\x1b') {
			t.Errorf("listLine(%q) = %q, want %q", c.l.OpID, got, c.want)
		}
	}
}
