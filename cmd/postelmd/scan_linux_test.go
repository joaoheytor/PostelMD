//go:build linux

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
			first := start(t, data) // creates the layout, app.db and an adopted vault
			setUp(t, first)
			first.shutdown(t)
			notes := filepath.Join(data, "vaults", "notes")
			must(t, os.WriteFile(filepath.Join(notes, "a.md"), []byte("A"), 0o600))
			if c.assign {
				assignIDs(t, data, scanVaultUUID, c.marker)
			}
			must(t, os.WriteFile(filepath.Join(notes, "a.md"), []byte("A, edited outside"), 0o600))
			must(t, os.WriteFile(filepath.Join(notes, "new.md"), []byte("new"), 0o600))
			r := start(t, data)
			logs := r.out.String() // ready: the first pass has run
			// A file the scan hasn't seen: its read gives it an ID (the router's read hook).
			must(t, os.WriteFile(filepath.Join(notes, "later.md"), []byte("later"), 0o600))
			if code := readNote(t, r, "later.md"); code != http.StatusOK {
				t.Fatalf("GET later.md = %d", code)
			}
			r.shutdown(t)
			if g, want := generationOf(t, data, "later.md"), map[bool]int64{true: 1}[c.scanned]; g != want {
				t.Fatalf("later.md at generation %d after its read, want %d", g, want)
			}
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

const scanPassword = "correct horse battery"

// setUp finishes the first-run setup of r with scanPassword.
func setUp(t *testing.T, r *running) {
	t.Helper()
	m := regexp.MustCompile(`one-time token\n\(valid 30m0s\): (\S+)`).FindStringSubmatch(r.out.String())
	if m == nil {
		t.Fatalf("no setup token printed:\n%s", r.out.String())
	}
	body := `{"token":"` + m[1] + `","password":"` + scanPassword + `","name":"Ada","email":"ada@example.invalid"}`
	if resp := post(t, r.origin+"/api/v1/setup", body); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup = %d", resp.StatusCode)
	}
}

// readNote logs in to r and reads the note p, returning the status.
func readNote(t *testing.T, r *running, p string) int {
	t.Helper()
	login := post(t, r.origin+"/api/v1/session", `{"password":"`+scanPassword+`"}`)
	if login.StatusCode != http.StatusNoContent {
		t.Fatalf("login = %d", login.StatusCode)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, r.origin+"/api/v1/notes/"+p, nil)
	must(t, err)
	for _, c := range login.Cookies() {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// startIDs skips the scan, with a warning, when the vault marker can't name the database's
// vault: format 1's empty marker, or a marker that isn't a regular file (ADR-0030's table).
func TestStartIDsSkipsAVaultItCantName(t *testing.T) {
	for _, name := range []string{"no marker", "an empty marker", "a symlinked marker"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			data := t.TempDir()
			start(t, data).shutdown(t)
			dir := filepath.Join(data, "vaults", "notes")
			must(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("A"), 0o600))
			assignIDs(t, data, scanVaultUUID, scanVaultUUID)
			must(t, os.WriteFile(filepath.Join(dir, "new.md"), []byte("new"), 0o600))
			v, err := vault.Open(dir)
			must(t, err)
			defer func() { _ = v.Close() }()
			marker := filepath.Join(dir, vault.TmpDir, ".created-by-postelmd")
			switch name {
			case "no marker":
				must(t, os.Remove(marker))
			case "an empty marker":
				must(t, os.WriteFile(marker, nil, 0o600))
			case "a symlinked marker": // a real marker, outside the vault
				elsewhere := t.TempDir()
				must(t, os.Mkdir(filepath.Join(elsewhere, vault.TmpDir), 0o700))
				must(t, vault.WriteMarker(elsewhere, scanVaultUUID, 1))
				must(t, os.Remove(marker))
				must(t, os.Symlink(filepath.Join(elsewhere, vault.TmpDir, ".created-by-postelmd"), marker))
			}
			db, err := sqlstore.Open(ctx, filepath.Join(data, ".postelmd"), slog.New(slog.DiscardHandler))
			must(t, err)
			defer func() { _ = db.Close() }()
			logs := &syncBuffer{}
			got, stop, err := startIDs(ctx, db, v, dir, slog.New(slog.NewJSONHandler(logs, nil)))
			must(t, err)
			stop()
			if got != nil || !strings.Contains(logs.String(), "the vault marker doesn't name its vault") {
				t.Fatalf("startIDs = %v; logs:\n%s", got, logs.String())
			}
			if _, err := db.FileByPath(ctx, "new.md"); !errors.Is(err, sqlstore.ErrNoFile) {
				t.Fatalf("new.md got a row: %v", err)
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
