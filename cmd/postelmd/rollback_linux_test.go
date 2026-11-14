package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/lock"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// listing is every name in dir with its size: what a refusal must leave as it was.
func listing(t *testing.T, dir string) string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range es {
		fi, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s:%s:%d ", e.Name(), fi.Mode(), fi.Size())
	}
	return b.String()
}

// db rollback after a start that finished its migrations refuses with the reason and changes
// nothing; next to a live server it refuses before reading anything; on newer data it
// refuses like every command (ADR-0025).
func TestDBRollbackRefuses(t *testing.T) {
	data := t.TempDir()
	r := start(t, data)
	held := runAndCheck(t, []string{"db", "rollback", "--data", data}, 1, "", "stop the server first")
	if !strings.Contains(held, "postelmd db rollback: ") {
		t.Errorf("stderr = %q, want it to name the command", held)
	}
	var he *lock.HeldError
	if err := dbRollback(r.cfg, &syncBuffer{}); !errors.As(err, &he) {
		t.Fatalf("db rollback next to a live server = %v, want HeldError", err)
	}
	r.shutdown(t)

	state := filepath.Join(data, ".postelmd")
	before := listing(t, state)
	runAndCheck(t, []string{"db", "rollback", "--data", data}, 1, "", "nothing to roll back")
	if err := dbRollback(r.cfg, &syncBuffer{}); !errors.Is(err, sqlstore.ErrRollbackRefused) {
		t.Fatalf("db rollback = %v, want ErrRollbackRefused", err)
	}
	if after := listing(t, state); after != before {
		t.Fatalf("the refusal changed the state directory:\nbefore %s\nafter  %s", before, after)
	}

	newer, _ := newerData(t)
	runAndCheck(t, []string{"db", "rollback", "--data", newer}, 1, "", "written by a newer PostelMD")
}

// db rollback on a data folder whose last migration was interrupted (the intent and the
// snapshot are in place, app.db is still at the earlier version): it exits 0, says what it
// did, retires migrate.intent, and the earlier build's database opens.
func TestDBRollbackUndoesAnInterruptedMigration(t *testing.T) {
	data := t.TempDir()
	state := filepath.Join(data, ".postelmd")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	last, err := sqlstore.LastMigration()
	if err != nil {
		t.Fatal(err)
	}
	from := last - 1
	ctx := t.Context()
	db, err := sqlstore.OpenEarlier(ctx, state, from, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	snap := fmt.Sprintf("pre-migrate-%d-%d.db", from, last)
	if _, err := db.Snapshot(ctx, filepath.Join(state, snap)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	intent := fmt.Sprintf(`{"from":%d,"to":%d,"snapshot":%q}`+"\n", from, last, snap)
	if err := os.WriteFile(filepath.Join(state, sqlstore.MigrateIntent), []byte(intent), 0o600); err != nil {
		t.Fatal(err)
	}

	want := fmt.Sprintf("Undid the migration from version %d to %d", from, last)
	runAndCheck(t, []string{"db", "rollback", "--data", data}, 0, want, "")
	if _, err := os.Lstat(filepath.Join(state, sqlstore.MigrateIntent)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migrate.intent after the rollback: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(state, snap)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the snapshot is still in the state directory: %v", err)
	}
	db, err = sqlstore.OpenEarlier(ctx, state, from, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("the earlier build's open after the rollback: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackMessage(t *testing.T) {
	for _, c := range []struct {
		r    sqlstore.RolledBack
		want string
	}{
		{sqlstore.RolledBack{From: 1, To: 2, Dir: "rollback-20261113T101500Z", Restored: true},
			"Undid the migration from version 1 to 2: app.db is again the version-1 copy taken before it. The database it replaced, its WAL files and that copy are in .postelmd/rollback-20261113T101500Z/. Start the previous version now.\n"},
		{sqlstore.RolledBack{From: 1, To: 2},
			"The migration from version 1 to 2 stopped before it changed the database: its intent is removed. Start the previous version now.\n"},
	} {
		if got := rollbackMessage(c.r); got != c.want {
			t.Errorf("rollbackMessage(%+v) = %q, want %q", c.r, got, c.want)
		}
	}
}
