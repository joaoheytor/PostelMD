//go:build linux

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/config"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// asAnotherUser makes the commands see an effective UID that doesn't own the data.
func asAnotherUser(t *testing.T) {
	t.Helper()
	old := geteuid
	geteuid = func() int { return os.Geteuid() + 1 }
	t.Cleanup(func() { geteuid = old })
}

// TestOfflineCommandsRefuseAnotherUser: at format 2 every command that changes the state with
// the server stopped refuses when its effective UID doesn't own .postelmd, before it changes
// anything (ADR-0042): what it created would belong to that user, and the server couldn't
// open it.
func TestOfflineCommandsRefuseAnotherUser(t *testing.T) {
	data := v01Data(t, false)
	state := filepath.Join(data, ".postelmd")
	atFormat2(t)
	asAnotherUser(t)
	cfg := config.Config{DataDir: data, Yes: true}
	// An empty stdin: a command that got past the check would read EOF, never block.
	stdin, err := os.Open(os.DevNull)
	must(t, err)
	t.Cleanup(func() { _ = stdin.Close() })
	before := listing(t, state)
	for name, run := range map[string]func() error{
		"db rebuild":     func() error { return dbRebuild(cfg, &syncBuffer{}) },
		"vault claim":    func() error { return vaultClaim(cfg, stdin, &syncBuffer{}) },
		"db rollback":    func() error { return dbRollback(cfg, &syncBuffer{}) },
		"reset-password": func() error { return resetPassword(cfg, stdin, &syncBuffer{}) },
	} {
		if err := run(); !errors.Is(err, layout.ErrNotOwner) {
			t.Errorf("%s as another user = %v, want ErrNotOwner", name, err)
		}
	}
	if after := listing(t, state); after != before {
		t.Fatalf("a refused command changed the state directory:\nbefore %s\nafter  %s", before, after)
	}
}

// TestOfflineCommandsRefuseAnotherUserBeforeCreatingAnything: with no .postelmd yet, the
// check is on the data directory, before the layout creates its folders, so a command run as
// another user (root without --user) creates nothing that the server couldn't open.
func TestOfflineCommandsRefuseAnotherUserBeforeCreatingAnything(t *testing.T) {
	atFormat2(t)
	asAnotherUser(t)
	stdin, err := os.Open(os.DevNull)
	must(t, err)
	t.Cleanup(func() { _ = stdin.Close() })
	for name, run := range map[string]func(config.Config) error{
		"db rebuild":     func(cfg config.Config) error { return dbRebuild(cfg, &syncBuffer{}) },
		"vault claim":    func(cfg config.Config) error { return vaultClaim(cfg, stdin, &syncBuffer{}) },
		"db rollback":    func(cfg config.Config) error { return dbRollback(cfg, &syncBuffer{}) },
		"reset-password": func(cfg config.Config) error { return resetPassword(cfg, stdin, &syncBuffer{}) },
	} {
		data := t.TempDir()
		if err := run(config.Config{DataDir: data, Yes: true}); !errors.Is(err, layout.ErrNotOwner) {
			t.Errorf("%s as another user = %v, want ErrNotOwner", name, err)
		}
		if got := listing(t, data); got != "" {
			t.Errorf("%s as another user created %s", name, got)
		}
	}
}

// TestTheOwnerCheckIsInertAtFormat1: a format-1 build runs db rollback as any user, as it
// always has.
func TestTheOwnerCheckIsInertAtFormat1(t *testing.T) {
	data := t.TempDir()
	start(t, data).shutdown(t)
	asAnotherUser(t)
	if err := dbRollback(config.Config{DataDir: data}, &syncBuffer{}); !errors.Is(err, sqlstore.ErrRollbackRefused) {
		t.Fatalf("db rollback at format 1 = %v, want its own refusal", err)
	}
}

// TestServeDamagedDatabaseNamesDBRebuildAtFormat2: a format-2 build names postelmd db
// rebuild for a damaged app.db (ADR-0034), on data at format 1 or 2, a command this build
// has; the files are unchanged.
func TestServeDamagedDatabaseNamesDBRebuildAtFormat2(t *testing.T) {
	for _, format := range []string{"1\n", "2\n"} {
		t.Run(strings.TrimSpace(format), func(t *testing.T) {
			data := t.TempDir()
			state := filepath.Join(data, ".postelmd")
			must(t, os.Mkdir(state, 0o700))
			junk := bytes.Repeat([]byte("not a database "), 512)
			must(t, os.WriteFile(filepath.Join(state, "app.db"), junk, 0o600))
			must(t, os.WriteFile(filepath.Join(state, "format"), []byte(format), 0o600))
			atFormat2(t)
			err := refusedStart(t, data)
			if !errors.Is(err, sqlstore.ErrDamaged) || !strings.Contains(err.Error(), "postelmd db rebuild") {
				t.Fatalf("serve on a damaged app.db = %v, want ErrDamaged naming db rebuild", err)
			}
			if strings.Contains(err.Error(), "into a new folder") {
				t.Fatalf("the refusal still gives format 1's manual fix: %v", err)
			}
			for _, m := range regexp.MustCompile("postelmd ([a-z]+ [a-z-]+)").FindAllStringSubmatch(err.Error(), -1) {
				if !strings.Contains(usage, "  "+m[1]+" ") {
					t.Errorf("the refusal names `postelmd %s`, which this build doesn't have", m[1])
				}
			}
			if b, rerr := os.ReadFile(filepath.Join(state, "app.db")); rerr != nil || !bytes.Equal(b, junk) { //nolint:gosec // G304: the test's data
				t.Fatalf("the damaged file was changed: %v", rerr)
			}
		})
	}
}
