//go:build linux

package layout

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDatabaseRefusesNetworkFS: POSTELMD_ALLOW_NETWORK_FS no longer covers the state
// directory once the database is involved (ADR-0030, "Files and how they are opened"): a
// command that opens app.db refuses to create it there, and once app.db exists every command
// refuses. The override still applies to the vault, and to a state directory with no database
// for a command that doesn't open one (reset-password before any v0.2 start).
func TestDatabaseRefusesNetworkFS(t *testing.T) {
	const ext4, nfs = 0xEF53, 0x6969
	for _, tt := range []struct {
		name         string
		state, vault int64
		opts         Options
		appDB        bool // an app.db already exists
		refused      bool
	}{
		{"state on nfs, opening the database: never create it", nfs, ext4, Options{AllowNetworkFS: true, OpensDatabase: true}, false, true},
		{"state on nfs, app.db exists, opening it", nfs, ext4, Options{AllowNetworkFS: true, OpensDatabase: true}, true, true},
		{"state on nfs, app.db exists, another command", nfs, ext4, Options{AllowNetworkFS: true}, true, true},
		{"state on nfs, no app.db, another command", nfs, ext4, Options{AllowNetworkFS: true}, false, false},
		{"a vault on nfs with the override, opening the database", ext4, nfs, Options{AllowNetworkFS: true, OpensDatabase: true}, false, false},
		{"both local, app.db exists", ext4, ext4, Options{OpensDatabase: true}, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fakeFS(t, tt.state, tt.vault)
			data := t.TempDir()
			if tt.appDB {
				if err := os.Mkdir(filepath.Join(data, ".postelmd"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(data, ".postelmd", "app.db"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := Prepare(data, tt.opts)
			if got := errors.Is(err, ErrDatabaseOnNetworkFS); got != tt.refused || (!tt.refused && err != nil) {
				t.Fatalf("Prepare = %v; want refused %v", err, tt.refused)
			}
			if tt.refused {
				if !strings.Contains(err.Error(), "local volume") || !strings.Contains(err.Error(), "/data/.postelmd") {
					t.Fatalf("the refusal doesn't name the fix: %v", err)
				}
				if _, serr := os.Lstat(filepath.Join(data, ".postelmd", "app.db")); tt.appDB == errors.Is(serr, os.ErrNotExist) {
					t.Fatalf("the refusal changed app.db: %v", serr)
				}
			}
		})
	}
}
