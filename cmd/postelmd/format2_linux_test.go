//go:build linux

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/config"
	"github.com/joaoheytor/PostelMD/internal/ids"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// atFormat2 makes this binary a build that writes data format 2 for one test: the format
// table, the first upgrade at start, the identity commands and the owner check run as they
// will once FormatVersion is 2 (part D of the IDs slice).
func atFormat2(t *testing.T) {
	t.Helper()
	old := buildFormat
	buildFormat = 2
	t.Cleanup(func() { buildFormat = old })
}

// v01Data is a data directory as a format-1 build leaves it: set up, with a.md and an empty
// marker, the format file written (or, with noFormat, removed: v0.1.0 data), app.db with no
// file IDs.
func v01Data(t *testing.T, noFormat bool) string {
	t.Helper()
	data := t.TempDir()
	r := start(t, data)
	setUp(t, r)
	r.shutdown(t)
	must(t, os.WriteFile(filepath.Join(vaultOf(data), "a.md"), []byte("A"), 0o600))
	if noFormat {
		must(t, os.Remove(filepath.Join(data, ".postelmd", "format")))
	}
	return data
}

func formatOf(t *testing.T, data string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(data, ".postelmd", "format")) //nolint:gosec // G304: the test's data
	if errors.Is(err, os.ErrNotExist) {
		return "<missing>"
	}
	must(t, err)
	return string(b)
}

func identityOf(t *testing.T, data string) sqlstore.Identity {
	t.Helper()
	db, err := sqlstore.Open(context.Background(), filepath.Join(data, ".postelmd"), slog.New(slog.DiscardHandler))
	must(t, err)
	defer func() { _ = db.Close() }()
	id, err := db.Identity(context.Background())
	must(t, err)
	return id
}

// refusedStart runs serve on data, which must refuse; it returns the error. A serve that
// starts instead is stopped after 30 s (it then returns nil) and fails the test.
func refusedStart(t *testing.T, data string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := serve(ctx, config.Config{DataDir: data, Listen: "127.0.0.1:0"}, &syncBuffer{}, nil)
	if err == nil {
		t.Fatal("serve started")
	}
	return err
}

// TestServeAtFormat2RunsTheFirstUpgrade: a format-2 build's first start on format-1 data (the
// format file there, or missing as v0.1.0 left it) runs the first upgrade before the scan:
// the "assigned" flag, the marker and format 2, pre-format-2/; then the scan. The next start
// goes on by the format table's "same UUID" row.
func TestServeAtFormat2RunsTheFirstUpgrade(t *testing.T) {
	for _, noFormat := range []bool{false, true} {
		t.Run(map[bool]string{false: "format 1", true: "no format file"}[noFormat], func(t *testing.T) {
			data := v01Data(t, noFormat)
			atFormat2(t)
			r := start(t, data)
			logs := r.out.String()
			r.shutdown(t)
			id := identityOf(t, data)
			m, err := vault.ReadMarker(vaultOf(data))
			if err != nil || !id.Assigned || m.VaultUUID != id.VaultUUID || m.Format != 2 {
				t.Fatalf("identity %+v, marker %+v (%v): want the first upgrade's", id, m, err)
			}
			if f := formatOf(t, data); f != "2\n" {
				t.Fatalf("format = %q, want 2", f)
			}
			if _, err := os.Stat(filepath.Join(data, ".postelmd", layout.PreFormat2, "complete")); err != nil {
				t.Fatalf("pre-format-2/ isn't complete: %v", err)
			}
			for _, want := range []string{`"msg":"first upgrade"`, `"files":1`, `"msg":"startup scan","pass":1`} {
				if !strings.Contains(logs, want) {
					t.Fatalf("the logs don't say %s:\n%s", want, logs)
				}
			}
			if strings.Contains(logs, "a.md") {
				t.Fatalf("a log record names a note:\n%s", logs)
			}
			again := start(t, data)
			logs = again.out.String()
			again.shutdown(t)
			if strings.Contains(logs, `"msg":"first upgrade"`) || !strings.Contains(logs, `"msg":"startup scan","pass":1`) {
				t.Fatalf("the second start upgraded again or didn't scan:\n%s", logs)
			}
		})
	}
}

// TestServeAtFormat2ResumesAnUpgrade: upgrade.intent there means a first upgrade to finish,
// whatever the format file says (killed after step 5 it says 2): serve resumes it.
func TestServeAtFormat2ResumesAnUpgrade(t *testing.T) {
	for _, format := range []string{"1\n", "2\n"} {
		t.Run(strings.TrimSpace(format), func(t *testing.T) {
			data := v01Data(t, false)
			state := filepath.Join(data, ".postelmd")
			atFormat2(t)
			_, err := sqlstore.BeginUpgrade(state, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
			must(t, err)
			must(t, os.WriteFile(filepath.Join(state, "format"), []byte(format), 0o600))
			r := start(t, data)
			logs := r.out.String()
			r.shutdown(t)
			if !strings.Contains(logs, `"resumed":true`) {
				t.Fatalf("no resumed upgrade in the logs:\n%s", logs)
			}
			if _, err := os.Lstat(filepath.Join(state, sqlstore.UpgradeIntent)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("upgrade.intent is still there: %v", err)
			}
			if f := formatOf(t, data); f != "2\n" || !identityOf(t, data).Assigned {
				t.Fatalf("format %q after the resumed upgrade", f)
			}
		})
	}
}

// TestServeAtFormat2RefusesByTheFormatTable: each refusing row of ADR-0030's format table
// stops the start before the vault opens, changing nothing, and names its fix.
func TestServeAtFormat2RefusesByTheFormatTable(t *testing.T) {
	const other = "00000000-0000-4000-8000-000000000000"
	upgraded := func(t *testing.T) string {
		t.Helper()
		data := v01Data(t, false)
		atFormat2(t)
		start(t, data).shutdown(t)
		return data
	}
	marker := markerOf
	for _, c := range []struct {
		name string
		data func(t *testing.T) string
		want error
		fix  string
		noDB bool // app.db must still be absent
	}{
		{"format 1, a marker naming a vault", func(t *testing.T) string {
			data := v01Data(t, false)
			must(t, vault.WriteMarker(vaultOf(data), other, 2))
			atFormat2(t)
			return data
		}, ids.ErrStateNotHere, "postelmd db rebuild", false},
		{"format 2, another vault's marker", func(t *testing.T) string {
			data := upgraded(t)
			must(t, vault.WriteMarker(vaultOf(data), other, 2))
			return data
		}, ids.ErrAnotherVault, "postelmd db rebuild", false},
		{"format 2, no marker, live rows", func(t *testing.T) string {
			data := upgraded(t)
			must(t, os.Remove(marker(data)))
			return data
		}, ids.ErrVaultNotClaimed, "postelmd vault claim", false},
		{"format 2, a marker that doesn't parse", func(t *testing.T) string {
			data := upgraded(t)
			must(t, os.WriteFile(marker(data), []byte("not ours\n"), 0o600))
			return data
		}, vault.ErrBadMarker, "postelmd vault claim", false},
		{"format 2, app.db lost", func(t *testing.T) string {
			data := upgraded(t)
			for _, n := range []string{"app.db", "app.db-wal", "app.db-shm"} {
				if err := os.Remove(filepath.Join(data, ".postelmd", n)); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
			return data
		}, sqlstore.ErrNoDatabase, "postelmd db rebuild", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := c.data(t)
			before, _ := os.ReadFile(marker(data)) //nolint:gosec // G304: the test's data
			format := formatOf(t, data)
			err := refusedStart(t, data)
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.fix) {
				t.Fatalf("serve = %v, want %v naming %q", err, c.want, c.fix)
			}
			if after, _ := os.ReadFile(marker(data)); string(after) != string(before) { //nolint:gosec // G304: the test's data
				t.Fatalf("the marker changed: %q, was %q", after, before)
			}
			if f := formatOf(t, data); f != format {
				t.Fatalf("the format changed: %q, was %q", f, format)
			}
			if _, err := os.Lstat(filepath.Join(data, ".postelmd", "app.db")); c.noDB && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("serve created app.db: %v", err)
			}
		})
	}
}

// TestServeAtFormat2KeepsServingWhenTheFirstPassFails is ADR-0036's decision 7 at format 2
// (ADR-0040): a first pass that fails (an unreadable folder) is logged, and serving goes on.
func TestServeAtFormat2KeepsServingWhenTheFirstPassFails(t *testing.T) {
	data := v01Data(t, false)
	atFormat2(t)
	start(t, data).shutdown(t)
	locked := filepath.Join(vaultOf(data), "locked")
	must(t, os.Mkdir(locked, 0o700))
	must(t, os.Chmod(locked, 0o300))                  //nolint:gosec // G302: a folder without its read bit, under test
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // G302: restored so TempDir can remove it
	r := start(t, data)
	logs := r.out.String()
	if code := readNote(t, r, "a.md"); code != 200 {
		t.Fatalf("GET a.md = %d after a failed first pass", code)
	}
	r.shutdown(t)
	if !strings.Contains(logs, "the startup scan failed") {
		t.Fatalf("no failed pass in the logs:\n%s", logs)
	}
}

// TestStartIDsRefusesAMismatchAtFormat2 is ADR-0036's decision 6 at format 2 (ADR-0040): the
// format table has run, so a marker that doesn't name the database's vault is an error, not
// a skipped scan with a warning (that stays for format 1: TestStartIDsSkipsAVaultItCantName).
func TestStartIDsRefusesAMismatchAtFormat2(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	start(t, data).shutdown(t)
	dir := vaultOf(data)
	assignIDs(t, data, scanVaultUUID, "00000000-0000-4000-8000-000000000000")
	atFormat2(t)
	v, err := vault.Open(dir)
	must(t, err)
	defer func() { _ = v.Close() }()
	db, err := sqlstore.Open(ctx, filepath.Join(data, ".postelmd"), slog.New(slog.DiscardHandler))
	must(t, err)
	defer func() { _ = db.Close() }()
	got, stop, err := startIDs(ctx, db, v, dir, slog.New(slog.DiscardHandler))
	stop()
	if got != nil || !errors.Is(err, ids.ErrAnotherVault) {
		t.Fatalf("startIDs = %v, %v; want ErrAnotherVault", got, err)
	}
}
