//go:build linux

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

const scanVaultUUID = "6f1c2a7e-3b4d-4c5e-8f90-a1b2c3d4e5f6"

// assignIDs gives the vault of data the first assignment with uuid and writes a marker
// naming markerUUID (part 4's upgrade does both; this build only scans), with no server
// running.
func assignIDs(t *testing.T, data, uuid, markerUUID string) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(data, "vaults", "notes")
	v, err := vault.Open(dir)
	must(t, err)
	obs, err := v.Survey(ctx)
	must(t, err)
	must(t, v.Close())
	db, err := sqlstore.Open(ctx, filepath.Join(data, ".postelmd"), slog.New(slog.DiscardHandler))
	must(t, err)
	a := sqlstore.Assignment{VaultUUID: uuid}
	for _, o := range obs {
		a.Files = append(a.Files, sqlstore.NewFile{Path: o.Path, SHA256: o.SHA256[:], Stat: sqlstore.FileStat(o.Stat)})
	}
	must(t, db.Assign(ctx, a))
	must(t, db.Close())
	must(t, vault.WriteMarker(dir, markerUUID, 1))
}

func generationOf(t *testing.T, data, p string) int64 {
	t.Helper()
	db, err := sqlstore.Open(context.Background(), filepath.Join(data, ".postelmd"), slog.New(slog.DiscardHandler))
	must(t, err)
	defer func() { _ = db.Close() }()
	f, err := db.FileByPath(context.Background(), p)
	if err != nil {
		return 0
	}
	return f.Generation
}

// serve runs the startup scan before it is ready when the database's IDs describe the
// mounted vault, and leaves everything as it is otherwise (format 1: nothing assigned; a
// marker naming another vault: format 2's table decides).
func TestServeRunsTheStartupScan(t *testing.T) {
	for _, c := range []struct {
		name    string
		assign  bool
		marker  string
		scanned bool
	}{
		{"assigned, this vault", true, scanVaultUUID, true},
		{"assigned, another vault's marker", true, "00000000-0000-4000-8000-000000000000", false},
		{"not assigned (format 1)", false, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := t.TempDir()
			start(t, data).shutdown(t) // creates the layout, app.db and an adopted vault
			notes := filepath.Join(data, "vaults", "notes")
			must(t, os.WriteFile(filepath.Join(notes, "a.md"), []byte("A"), 0o600))
			if c.assign {
				assignIDs(t, data, scanVaultUUID, c.marker)
			}
			must(t, os.WriteFile(filepath.Join(notes, "a.md"), []byte("A, edited outside"), 0o600))
			must(t, os.WriteFile(filepath.Join(notes, "new.md"), []byte("new"), 0o600))
			r := start(t, data)
			logs := r.out.String() // ready: the first pass has run
			r.shutdown(t)
			ran := strings.Contains(logs, `"msg":"startup scan","pass":1,"new_ids":1,"generation_bumps":1`)
			if ran != c.scanned {
				t.Fatalf("scanned = %v, want %v; logs:\n%s", ran, c.scanned, logs)
			}
			wantGen, wantNew := int64(0), int64(0)
			if c.assign {
				wantGen = 1
			}
			if c.scanned {
				wantGen, wantNew = 2, 1
			}
			if g, n := generationOf(t, data, "a.md"), generationOf(t, data, "new.md"); g != wantGen || n != wantNew {
				t.Fatalf("a.md at generation %d, new.md at %d; want %d, %d", g, n, wantGen, wantNew)
			}
			if strings.Contains(logs, "a.md") || strings.Contains(logs, "new.md") {
				t.Fatalf("a log record names a note:\n%s", logs)
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
