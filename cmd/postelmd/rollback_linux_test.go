package main

import (
	"errors"
	"fmt"
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

func TestRollbackMessage(t *testing.T) {
	for _, c := range []struct {
		r    sqlstore.RolledBack
		want string
	}{
		{sqlstore.RolledBack{From: 1, To: 2, Dir: "rollback-20261113T101500Z", Restored: true},
			"Rolled back the database from version 2 to 1. The database it replaced, its WAL files and the snapshot are in .postelmd/rollback-20261113T101500Z/. Start the previous version now.\n"},
		{sqlstore.RolledBack{From: 1, To: 2},
			"The migration from version 1 to 2 stopped before it changed the database: its intent is removed. Start the previous version now.\n"},
	} {
		if got := rollbackMessage(c.r); got != c.want {
			t.Errorf("rollbackMessage(%+v) = %q, want %q", c.r, got, c.want)
		}
	}
}
