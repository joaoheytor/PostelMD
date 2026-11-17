//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/ids"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// tree lists every entry under dir with its kind and size, for a before/after comparison.
func tree(t *testing.T, dir string) string {
	t.Helper()
	var out []string
	must(t, filepath.WalkDir(dir, func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		size := fi.Size()
		if d.IsDir() {
			size = 0 // a folder's size changes with its entries, which are listed anyway
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, fmt.Sprintf("%s %v %d", rel, fi.Mode().Type(), size))
		return nil
	}))
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// TestServeAtFormat2RewritesAnEmptyMarker: at format 2 an empty marker (ADR-0027's printed
// fix recreated it) on the database's own vault passes the identity check: serve rewrites
// it with the stored UUID, warns, and starts.
func TestServeAtFormat2RewritesAnEmptyMarker(t *testing.T) {
	data := v01Data(t, false)
	atFormat2(t)
	start(t, data).shutdown(t)
	must(t, os.WriteFile(markerOf(data), nil, 0o600))
	r := start(t, data)
	logs := r.out.String()
	if code := readNote(t, r, "a.md"); code != 200 {
		t.Fatalf("GET a.md = %d", code)
	}
	r.shutdown(t)
	m, err := vault.ReadMarker(vaultOf(data))
	if id := identityOf(t, data); err != nil || m.VaultUUID != id.VaultUUID || m.Format != 2 {
		t.Fatalf("marker %+v (%v), want the stored UUID %s", m, err, id.VaultUUID)
	}
	if !strings.Contains(logs, `"level":"WARN","msg":"rewrote the vault marker`) {
		t.Fatalf("no warning for the rewritten marker:\n%s", logs)
	}
}

// TestServeAtFormat2RefusesAFormat1StartBeforeTheVaultOpens: each refusal of the first upgrade
// (ADR-0041's, ADR-0030's format-1 rows) comes before the vault opens, so nothing in the vault
// changes: an in-flight temp in .postelmd-tmp/ survives (no sweep), no probe file and no
// adoption, the marker as it was.
func TestServeAtFormat2RefusesAFormat1StartBeforeTheVaultOpens(t *testing.T) {
	const other = "00000000-0000-4000-8000-000000000000"
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	assignFlag := func(t *testing.T, data string) {
		db, err := sqlstore.Open(context.Background(), filepath.Join(data, ".postelmd"), slog.New(slog.DiscardHandler))
		must(t, err)
		must(t, db.Assign(context.Background(), sqlstore.Assignment{VaultUUID: other, Now: now}))
		must(t, db.Close())
	}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, data string)
		want  error
	}{
		{"a marker naming a vault", func(t *testing.T, data string) {
			must(t, vault.WriteMarker(vaultOf(data), other, 2))
		}, ids.ErrStateNotHere},
		{"the flag set without an intent", assignFlag, sqlstore.ErrUpgradeInterference},
		{"a marker that is a folder", func(t *testing.T, data string) {
			must(t, os.Remove(markerOf(data)))
			must(t, os.Mkdir(markerOf(data), 0o700))
		}, vault.ErrMarkerNotAFile},
		{"a resumed upgrade, another vault's marker", func(t *testing.T, data string) {
			_, err := sqlstore.BeginUpgrade(filepath.Join(data, ".postelmd"), now)
			must(t, err)
			must(t, vault.WriteMarker(vaultOf(data), other, 2))
		}, ids.ErrAnotherVault},
		{"a resumed upgrade, the flag under another UUID", func(t *testing.T, data string) {
			_, err := sqlstore.BeginUpgrade(filepath.Join(data, ".postelmd"), now)
			must(t, err)
			assignFlag(t, data)
		}, sqlstore.ErrUpgradeInterference},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := v01Data(t, false)
			c.setup(t, data)
			// A save in flight when the last server stopped: the sweep would remove it.
			must(t, os.WriteFile(filepath.Join(vaultOf(data), vault.TmpDir, "inflight.tmp"), []byte("draft"), 0o600))
			atFormat2(t)
			before := tree(t, vaultOf(data))
			err := refusedStart(t, data)
			if !errors.Is(err, c.want) {
				t.Fatalf("serve = %v, want %v", err, c.want)
			}
			if after := tree(t, vaultOf(data)); after != before {
				t.Fatalf("the refused start changed the vault:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}
