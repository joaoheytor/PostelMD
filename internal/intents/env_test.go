//go:build linux

package intents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/audit"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

var ctx = context.Background()

const opA = "0123456789abcdef0123456789abcdef"

var t0 = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

// env is a vault holding notes/a.md ("base") with its ID assigned, and the logs recovery
// writes, to check that no path reaches them.
type env struct {
	t          *testing.T
	dir, state string
	v          *vault.Vault
	db         *sqlstore.DB
	logs       bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, dir: filepath.Join(t.TempDir(), "vault"), state: filepath.Join(t.TempDir(), "state")}
	must(t, os.MkdirAll(filepath.Join(e.dir, "notes"), 0o700))
	must(t, os.Mkdir(e.state, 0o700))
	e.write("notes/a.md", "base")
	e.open()
	obs, err := e.v.Survey(ctx)
	must(t, err)
	a := sqlstore.Assignment{VaultUUID: "6f1c2a7e-3b4d-4c5e-8f90-a1b2c3d4e5f6", Now: t0}
	for _, o := range obs {
		st := o.Stat
		a.Files = append(a.Files, sqlstore.NewFile{Path: o.Path, SHA256: o.SHA256[:], Stat: sqlstore.FileStat{Size: st.Size,
			Device: st.Device, Inode: st.Inode, MtimeNS: st.MtimeNS, CtimeNS: st.CtimeNS}})
	}
	must(t, e.db.Assign(ctx, a))
	return e
}

func (e *env) open() {
	var err error
	e.v, err = vault.Open(e.dir)
	must(e.t, err)
	e.db, err = sqlstore.Open(ctx, e.state, slog.New(slog.DiscardHandler))
	must(e.t, err)
	e.t.Cleanup(func() { _ = e.db.Close(); _ = e.v.Close() })
}

func (e *env) recover() Totals {
	e.t.Helper()
	log := e.logger()
	tot, err := Recover(ctx, Recovery{DB: e.db, Vault: e.v, State: e.state, Log: log, Audit: audit.New(log)})
	must(e.t, err)
	return tot
}

func (e *env) logger() *slog.Logger { return slog.New(slog.NewJSONHandler(&e.logs, nil)) }

func (e *env) write(p, content string) {
	e.t.Helper()
	full := filepath.Join(e.dir, p)
	must(e.t, os.MkdirAll(filepath.Dir(full), 0o700))
	must(e.t, os.WriteFile(full, []byte(content), 0o600)) //nolint:gosec // G703: the test's own vault
}

func (e *env) temp() string { return vault.TmpDir + "/" + opA }

func (e *env) exists(p string) bool {
	_, err := os.Lstat(filepath.Join(e.dir, p))
	return err == nil
}

func (e *env) read(p string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir, p)) //nolint:gosec // G304: the test's own vault
	must(e.t, err)
	return string(b)
}

// copies returns recovery/<op-id>/'s files and their content.
func (e *env) copies() map[string]string {
	out := map[string]string{}
	dir := filepath.Join(e.state, "recovery", opA)
	entries, _ := os.ReadDir(dir)
	for _, d := range entries {
		b, err := os.ReadFile(filepath.Join(dir, d.Name())) //nolint:gosec // G304: the test's own state
		must(e.t, err)
		out[d.Name()] = string(b)
	}
	return out
}

func (e *env) row() sqlstore.File {
	e.t.Helper()
	all, err := e.db.Files(ctx)
	must(e.t, err)
	if len(all) != 1 {
		e.t.Fatalf("%d rows, want a.md's", len(all))
	}
	return all[0]
}

// saveIntent begins a save of notes/a.md from "base" to "new", as the save does before its
// temp (ADR-0033's save step 2).
func (e *env) saveIntent() sqlstore.File {
	e.t.Helper()
	r := e.row()
	sum := sha256.Sum256([]byte("new"))
	must(e.t, e.db.Begin(ctx, opA, sqlstore.SaveIntent{FileID: r.ID, Path: "notes/a.md", BaseGeneration: r.Generation,
		BaseSHA256: r.SHA256, NewSHA256: sum[:], Stat: r.Stat}, t0))
	return r
}

// intent returns the only intent, or ok false.
func (e *env) intent() (sqlstore.Intent, bool) {
	e.t.Helper()
	in, err := e.db.Intents(ctx)
	must(e.t, err)
	if len(in) > 1 {
		e.t.Fatalf("%d intents", len(in))
	}
	if len(in) == 0 {
		return sqlstore.Intent{}, false
	}
	return in[0], true
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
