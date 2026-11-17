//go:build linux

package ids

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

var rebuildNow = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

// rebuildEnv is a vault with IDs (a.md, sub/b.md, one trash item), two unfinished saves'
// temps, a folder, a symlink and a FIFO in the temp folder, and its database closed: a
// server stopped for db rebuild.
func rebuildEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, map[string]string{"a.md": "a", "sub/b.md": "b", "gone.md": "g"})
	if _, err := e.v.Delete(ctx, "gone.md"); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(e.dir, vault.TmpDir)
	must(t, os.WriteFile(filepath.Join(tmp, "save-1"), []byte("draft one"), 0o600))
	must(t, os.WriteFile(filepath.Join(tmp, "save-2"), []byte("draft two"), 0o600))
	must(t, os.Mkdir(filepath.Join(tmp, "dir"), 0o700))
	must(t, os.Symlink("/etc/passwd", filepath.Join(tmp, "link")))
	must(t, syscall.Mkfifo(filepath.Join(tmp, "fifo"), 0o600))
	e.close()
	return e
}

func rebuildOpts(e *env) RebuildOptions {
	return RebuildOptions{State: e.state, Vault: e.dir, Format: 1, Now: rebuildNow, Log: slog.New(slog.DiscardHandler)}
}

// checkRebuilt asserts what a finished rebuild leaves: no intent; a database with the
// rebuild's UUID, a new vault UUID that the marker names, a row per file and the trash item;
// the old database in the damaged folder; both temps kept, and the temp folder swept.
func checkRebuilt(t *testing.T, e *env, rebuildUUID string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(e.state, sqlstore.RebuildIntent)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rebuild.intent is still there: %v", err)
	}
	e.open()
	id, err := e.db.Identity(ctx)
	must(t, err)
	if !id.Assigned || id.RebuildUUID != rebuildUUID || id.VaultUUID == testVaultUUID {
		t.Fatalf("identity %+v, want the rebuild %s and a new vault UUID", id, rebuildUUID)
	}
	m, err := vault.ReadMarker(e.dir)
	if err != nil || m.VaultUUID != id.VaultUUID || m.Format != 1 {
		t.Fatalf("marker %+v, %v; want %s", m, err, id.VaultUUID)
	}
	e.row("a.md")
	e.row("sub/b.md")
	e.noRow("gone.md")
	if recs, err := e.db.TrashRecords(ctx); err != nil || len(recs) != 1 {
		t.Fatalf("trash records %d, %v; want 1", len(recs), err)
	}
	stamp := "20261117T100000Z"
	if _, err := os.Stat(filepath.Join(e.state, "damaged-"+stamp, sqlstore.AppDB)); err != nil {
		t.Fatalf("the old database isn't in the damaged folder: %v", err)
	}
	for n, want := range map[string]string{"save-1": "draft one", "save-2": "draft two"} {
		b, err := os.ReadFile(filepath.Join(e.state, "recovery", "rebuild-"+stamp, n)) //nolint:gosec // G304: the test's state
		if err != nil || string(b) != want {
			t.Fatalf("kept %s = %q, %v", n, b, err)
		}
		if _, err := os.Lstat(filepath.Join(e.dir, vault.TmpDir, n)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the temp %s is still in the vault: %v", n, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(e.state, "format")); err != nil || string(b) != "1\n" {
		t.Fatalf("format %q, %v", b, err)
	}
}

func TestRebuild(t *testing.T) {
	e := rebuildEnv(t)
	r, err := Rebuild(ctx, rebuildOpts(e))
	must(t, err)
	if r.Resumed || !r.HadDB || r.Files != 2 || r.Trash != 1 || r.Kept != 2 || r.Skipped != 3 {
		t.Fatalf("Rebuilt = %+v", r)
	}
	checkRebuilt(t, e, r.UUID)
	if id, err := e.db.Identity(ctx); err != nil || r.VaultUUID != id.VaultUUID {
		t.Fatalf("Rebuilt names the vault UUID %s, the database %+v (%v)", r.VaultUUID, id, err)
	}
}

// TestRebuildKeepsATempOverTheReadLimit: every regular temp is copied (ADR-0030), one over
// fs.MaxFileSize too, by streaming it.
func TestRebuildKeepsATempOverTheReadLimit(t *testing.T) {
	e := rebuildEnv(t)
	big := filepath.Join(e.dir, vault.TmpDir, "big")
	f, err := os.Create(big) //nolint:gosec // G304: the test's vault
	must(t, err)
	_, err = f.WriteAt([]byte("end"), fs.MaxFileSize)
	must(t, errors.Join(err, f.Close()))
	r, err := Rebuild(ctx, rebuildOpts(e))
	must(t, err)
	if r.Kept != 3 {
		t.Fatalf("kept %d temps, want 3", r.Kept)
	}
	fi, err := os.Stat(filepath.Join(e.state, r.RecoveryDir(), "big"))
	if err != nil || fi.Size() != fs.MaxFileSize+3 {
		t.Fatalf("the big temp's copy: %v, %v", fi, err)
	}
}

// TestRebuildWithoutADatabase: a lost app.db is the usual case; nothing is moved and no
// damaged folder is made.
func TestRebuildWithoutADatabase(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "a"})
	e.close()
	for _, n := range []string{"app.db", "app.db-wal", "app.db-shm"} {
		_ = os.Remove(filepath.Join(e.state, n))
	}
	r, err := Rebuild(ctx, rebuildOpts(e))
	must(t, err)
	if r.HadDB || r.Files != 1 {
		t.Fatalf("Rebuilt = %+v", r)
	}
	if _, err := os.Lstat(filepath.Join(e.state, r.DamagedDir())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a damaged folder was made with nothing to move: %v", err)
	}
	e.open()
	e.row("a.md")
}

// TestRebuildRefusesBeforeItsFirstStep: a marker that is a symlink, a vault that isn't there,
// or another operation's intent: refused, with no intent written and nothing moved.
func TestRebuildRefusesBeforeItsFirstStep(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(e *env) RebuildOptions
	}{
		{"marker symlink", func(e *env) RebuildOptions {
			must(t, os.Remove(markerPath(e.dir)))
			must(t, os.Symlink("x", markerPath(e.dir)))
			return rebuildOpts(e)
		}},
		{"no vault", func(e *env) RebuildOptions {
			o := rebuildOpts(e)
			o.Vault = filepath.Join(e.dir, "missing")
			return o
		}},
		{"rollback.intent", func(e *env) RebuildOptions {
			must(t, os.WriteFile(filepath.Join(e.state, sqlstore.RollbackIntent), []byte("{}"), 0o600))
			return rebuildOpts(e)
		}},
		{"a trash adoption refuses", func(e *env) RebuildOptions { // no marker, and the trash isn't empty
			must(t, os.Remove(markerPath(e.dir)))
			must(t, os.MkdirAll(filepath.Join(e.dir, ".postelmd-trash"), 0o700))
			must(t, os.WriteFile(filepath.Join(e.dir, ".postelmd-trash", "not-ours"), []byte("x"), 0o600))
			return rebuildOpts(e)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, map[string]string{"a.md": "a"})
			e.close()
			_, err := Rebuild(ctx, tc.setup(e))
			if !errors.Is(err, sqlstore.ErrRebuildRefused) {
				t.Fatalf("Rebuild = %v, want ErrRebuildRefused", err)
			}
			for _, n := range []string{sqlstore.RebuildIntent, "damaged-20261117T100000Z"} {
				if _, err := os.Lstat(filepath.Join(e.state, n)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s is there after a refusal: %v", n, err)
				}
			}
		})
	}
}
