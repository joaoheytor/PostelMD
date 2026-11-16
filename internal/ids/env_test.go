//go:build linux

package ids

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

const testVaultUUID = "6f1c2a7e-3b4d-4c5e-8f90-a1b2c3d4e5f6"

var ctx = context.Background()

// env is a real vault in a temp folder and a real app.db, with IDs assigned to what was in
// the vault at the start, and a scanner on a clock the test moves.
type env struct {
	t     *testing.T
	dir   string // the vault
	state string // the state directory
	v     *vault.Vault
	db    *sqlstore.DB
	s     *Scanner
	clock time.Time
}

func newEnv(t *testing.T, files map[string]string) *env {
	t.Helper()
	e := &env{t: t, dir: filepath.Join(t.TempDir(), "vault"), state: filepath.Join(t.TempDir(), "state"),
		clock: time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)}
	must(t, os.Mkdir(e.dir, 0o700))
	must(t, os.Mkdir(e.state, 0o700))
	for p, c := range files {
		e.write(p, c)
	}
	e.open()
	obs, err := e.v.Survey(ctx)
	must(t, err)
	a := sqlstore.Assignment{VaultUUID: testVaultUUID, Now: e.clock}
	for _, o := range obs {
		a.Files = append(a.Files, sqlstore.NewFile{Path: o.Path, SHA256: o.SHA256[:], Stat: fileStat(o.Stat)})
	}
	must(t, e.db.Assign(ctx, a))
	return e
}

// openEnv opens an existing vault and state directory (a restart, or the crash helper),
// with the clock at now.
func openEnv(t *testing.T, dir, state string, now time.Time) *env {
	e := &env{t: t, dir: dir, state: state, clock: now}
	e.open()
	return e
}

// open opens the vault and the database (again, after close: a restart).
func (e *env) open() {
	var err error
	e.v, err = vault.Open(e.dir)
	must(e.t, err)
	e.db, err = sqlstore.Open(ctx, e.state, slog.New(slog.DiscardHandler))
	must(e.t, err)
	e.s = New(e.db, e.v, slog.New(slog.DiscardHandler))
	e.s.now = func() time.Time { return e.clock }
	e.t.Cleanup(e.close)
}

func (e *env) close() {
	if e.db != nil {
		_ = e.db.Close()
		_ = e.v.Close()
		e.db, e.v = nil, nil
	}
}

func (e *env) after(d time.Duration) { e.clock = e.clock.Add(d) }

func (e *env) pass() Totals {
	e.t.Helper()
	tot, err := e.s.Pass(ctx)
	must(e.t, err)
	return tot
}

func (e *env) write(p, content string) {
	e.t.Helper()
	full := filepath.Join(e.dir, p)
	must(e.t, os.MkdirAll(filepath.Dir(full), 0o700))
	must(e.t, os.WriteFile(full, []byte(content), 0o600)) //nolint:gosec // G703: the test's own vault
}

func (e *env) mv(from, to string) {
	e.t.Helper()
	must(e.t, os.MkdirAll(filepath.Dir(filepath.Join(e.dir, to)), 0o700))
	must(e.t, os.Rename(filepath.Join(e.dir, from), filepath.Join(e.dir, to)))
}

// aside moves p out of the vault (the same inode, nowhere a scan looks); back returns it.
func (e *env) aside(p string) {
	e.t.Helper()
	must(e.t, os.Rename(filepath.Join(e.dir, p), filepath.Join(e.state, "aside")))
}

func (e *env) back(p string) {
	e.t.Helper()
	must(e.t, os.Rename(filepath.Join(e.state, "aside"), filepath.Join(e.dir, p)))
}

func (e *env) rm(p string) {
	e.t.Helper()
	must(e.t, os.Remove(filepath.Join(e.dir, p)))
}

// cp copies from to a new file to: a new inode and a new mtime.
func (e *env) cp(from, to string) {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir, from)) //nolint:gosec // G304: the test's vault
	must(e.t, err)
	e.write(to, string(b))
}

// row returns the row at p; it fails the test if there is none.
func (e *env) row(p string) sqlstore.File {
	e.t.Helper()
	f, err := e.db.FileByPath(ctx, p)
	if err != nil {
		e.t.Fatalf("FileByPath(%q): %v", p, err)
	}
	return f
}

func (e *env) noRow(p string) {
	e.t.Helper()
	if f, err := e.db.FileByPath(ctx, p); !errors.Is(err, sqlstore.ErrNoFile) {
		e.t.Fatalf("FileByPath(%q) = %+v, %v; want no row", p, f, err)
	}
}

func (e *env) holds() []sqlstore.Pending {
	e.t.Helper()
	h, err := e.db.Holds(ctx)
	must(e.t, err)
	return h
}

func (e *env) tombstone(id string) sqlstore.Tombstone {
	e.t.Helper()
	ts, err := e.db.TombstoneByID(ctx, id)
	if err != nil {
		e.t.Fatalf("TombstoneByID(%s): %v", id, err)
	}
	return ts
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
