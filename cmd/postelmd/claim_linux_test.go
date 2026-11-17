package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/config"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// identityReady turns db rebuild and vault claim on for one test, as data format 2 will.
func identityReady(t *testing.T) {
	t.Helper()
	old := identityCommandsReady
	identityCommandsReady = true
	t.Cleanup(func() { identityCommandsReady = old })
}

func vaultOf(data string) string { return filepath.Join(data, "vaults", "notes") }

func markerOf(data string) string {
	return filepath.Join(vaultOf(data), vault.TmpDir, ".created-by-postelmd")
}

// rebuiltData is a data directory a server ran on once, with two notes, then db rebuild: a
// database with IDs and a marker naming its vault.
func rebuiltData(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
	start(t, data).shutdown(t)
	for _, n := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(vaultOf(data), n), []byte(n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runAndCheck(t, []string{"db", "rebuild", "--data", data}, 0, "2 files have new IDs", "")
	return data
}

// TestTheIdentityCommandsRefuseBeforeFormat2: at data format 1 both commands refuse and
// create nothing, not even the data directory's folders (ADR-0040).
func TestTheIdentityCommandsRefuseBeforeFormat2(t *testing.T) {
	for _, args := range [][]string{{"db", "rebuild"}, {"vault", "claim", "--yes"}} {
		data := filepath.Join(t.TempDir(), "data")
		runAndCheck(t, append(args, "--data", data), 1, "", "arrives with data format 2")
		if _, err := os.Lstat(data); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%v made the data directory: %v", args, err)
		}
	}
}

// TestDBRebuildCommand: it makes a database whose IDs serve then scans, says where the old
// one went, and refuses next to a live server.
func TestDBRebuildCommand(t *testing.T) {
	identityReady(t)
	data := t.TempDir()
	start(t, data).shutdown(t)
	var stdout, stderr strings.Builder
	if code := run([]string{"db", "rebuild", "--data", data}, &stdout, &stderr); code != 0 {
		t.Fatalf("db rebuild: exit %d, %s", code, stderr.String())
	}
	for _, want := range []string{"Made a new database", ".postelmd/damaged-", "Start the server now."} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want %q", stdout.String(), want)
		}
	}
	m, err := vault.ReadMarker(vaultOf(data))
	if err != nil || m.VaultUUID == "" {
		t.Fatalf("marker %+v, %v", m, err)
	}
	r := start(t, data)
	runAndCheck(t, []string{"db", "rebuild", "--data", data}, 1, "", "stop the server first")
	r.shutdown(t)
	if strings.Contains(r.out.String(), "the startup scan is skipped") {
		t.Fatal("serve skipped the scan of the rebuilt database")
	}
}

// TestVaultClaimCommand: with the marker emptied, claim needs --yes (no terminal to ask on)
// and then writes it; a second run has nothing to do. When the identity check fails, --yes
// isn't enough and --force is.
func TestVaultClaimCommand(t *testing.T) {
	identityReady(t)
	data := rebuiltData(t)
	m, _ := vault.ReadMarker(vaultOf(data))
	empty := func() {
		if err := os.WriteFile(markerOf(data), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	empty()
	runAndCheck(t, []string{"vault", "claim", "--data", data}, 1, "identity check passes", "add --yes")
	if b, _ := os.ReadFile(markerOf(data)); len(b) != 0 {
		t.Fatal("a refused claim wrote the marker")
	}
	runAndCheck(t, []string{"vault", "claim", "--yes", "--data", data}, 0, "File IDs checked: 2, found at their path with the same inode and size: 2", "")
	if got, err := vault.ReadMarker(vaultOf(data)); err != nil || got.VaultUUID != m.VaultUUID {
		t.Fatalf("after the claim: %+v, %v; want %s", got, err, m.VaultUUID)
	}
	runAndCheck(t, []string{"vault", "claim", "--data", data}, 0, "nothing to do", "")

	for _, n := range []string{"a.md", "b.md"} { // new inodes: nothing matches
		replaceFile(t, filepath.Join(vaultOf(data), n))
	}
	if err := vault.WriteMarker(vaultOf(data), "00000000-0000-4000-8000-000000000000", 1); err != nil {
		t.Fatal(err)
	}
	runAndCheck(t, []string{"vault", "claim", "--yes", "--data", data}, 1,
		"The vault marker names another vault: 00000000-0000-4000-8000-000000000000", "add --force")
	runAndCheck(t, []string{"vault", "claim", "--data", data}, 1, "found at their path with the same inode and size: 0", "add --force")
	runAndCheck(t, []string{"vault", "claim", "--force", "--data", data}, 0, `"event":"vault.claimed"`, "")
}

// replaceFile gives p a new inode with the same bytes: a copy written beside it while it
// exists (so the filesystem can't reuse its inode), then renamed over it.
func replaceFile(t *testing.T, p string) {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // G304: the test's vault
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p+".new", b, 0o600); err != nil { //nolint:gosec // G703: the test's vault
		t.Fatal(err)
	}
	if err := os.Rename(p+".new", p); err != nil {
		t.Fatal(err)
	}
}

// TestTheIdentityCommandsAudit: db rebuild and vault claim each write one audit record to
// stdout, the sink serve uses; vault claim's says how it was confirmed.
func TestTheIdentityCommandsAudit(t *testing.T) {
	identityReady(t)
	data := t.TempDir()
	start(t, data).shutdown(t)
	var out strings.Builder
	if code := run([]string{"db", "rebuild", "--data", data}, &out, &strings.Builder{}); code != 0 ||
		strings.Count(out.String(), `"event":"db.rebuilt"`) != 1 {
		t.Fatalf("db rebuild: exit %d, stdout %s", code, out.String())
	}
	if err := os.WriteFile(markerOf(data), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"vault", "claim", "--force", "--data", data}, &out, &strings.Builder{}); code != 0 ||
		!strings.Contains(out.String(), `"event":"vault.claimed"`) || !strings.Contains(out.String(), `"force":true`) {
		t.Fatalf("vault claim: exit %d, stdout %s", code, out.String())
	}
}

// TestVaultClaimAsks: on a terminal, "yes" claims when the check passes, the vault's path
// when it fails; any other answer changes nothing.
func TestVaultClaimAsks(t *testing.T) {
	identityReady(t)
	old := isTerminal
	isTerminal = func(int) bool { return true }
	t.Cleanup(func() { isTerminal = old })
	data := rebuiltData(t)
	cfg := config.Config{DataDir: data}
	var prompt strings.Builder
	answer := func(s string) error {
		t.Helper()
		if err := os.WriteFile(markerOf(data), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		_, _ = w.WriteString(s)
		_ = w.Close()
		prompt.Reset()
		return vaultClaim(cfg, r, &prompt)
	}
	if err := answer("no\n"); !errors.Is(err, errDeclined) {
		t.Fatalf("answer no: %v, want errDeclined", err)
	}
	if want := "Type yes (or the vault's path) to claim it: "; !strings.Contains(prompt.String(), want) {
		t.Fatalf("the prompt %q lacks %q", prompt.String(), want)
	}
	if b, _ := os.ReadFile(markerOf(data)); len(b) != 0 {
		t.Fatal("a declined claim wrote the marker")
	}
	if err := answer("yes\n"); err != nil {
		t.Fatalf("answer yes: %v", err)
	}
	if err := answer(vaultOf(data) + "\n"); err != nil {
		t.Fatalf("the vault's path when the check passes: %v", err)
	}
	if err := os.Remove(filepath.Join(vaultOf(data), "a.md")); err != nil { // 1 of 2: the check fails
		t.Fatal(err)
	}
	if err := answer("yes\n"); !errors.Is(err, errDeclined) {
		t.Fatalf("yes to a failed check: %v, want errDeclined", err)
	}
	if want := "Type the vault's path (" + vaultOf(data) + ") to claim it anyway: "; !strings.Contains(prompt.String(), want) {
		t.Fatalf("the prompt %q lacks %q", prompt.String(), want)
	}
	if err := answer(vaultOf(data) + "\n"); err != nil {
		t.Fatalf("the vault's path: %v", err)
	}
	if !strings.Contains(prompt.String(), `"typed_path":true`) {
		t.Fatalf("the audit record doesn't say the path was typed: %s", prompt.String())
	}
}
