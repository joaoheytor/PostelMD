//go:build linux

package ids

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// TestRebuildCrashHelper runs db rebuild on -ids.crash-vault and -ids.crash-state and is
// SIGKILLed at -ids.crash-at ("point" or "point:n", the nth time; CLAUDE.md rule 14).
func TestRebuildCrashHelper(t *testing.T) {
	if *crashAt == "" {
		t.Skip("run by TestRebuildCrashPoints")
	}
	name, nth, _ := strings.Cut(*crashAt, ":")
	want, _ := strconv.Atoi(nth)
	want = max(want, 1)
	seen := 0
	killPoint = func(p string) {
		if p == name {
			if seen++; seen == want {
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
				select {} // the signal is on its way
			}
		}
	}
	r, err := Rebuild(ctx, RebuildOptions{State: *crashState, Vault: *crashVault, Format: 1, Now: rebuildNow,
		Log: slog.New(slog.DiscardHandler)})
	t.Fatalf("not killed at %s (Rebuild: %+v, %v)", *crashAt, r, err)
}

func killRebuildAt(t *testing.T, e *env, at string) {
	t.Helper()
	out, err := exec.Command(os.Args[0], "-test.run=^TestRebuildCrashHelper$", "-test.count=1", //nolint:gosec // G204: this test binary, with its own flags
		"-ids.crash-at="+at, "-ids.crash-vault="+e.dir, "-ids.crash-state="+e.state).CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("the helper wasn't killed at %s: %v\n%s", at, err, out)
	}
}

// The recovery table of db rebuild (ADR-0040), at the steps internal/ids runs; the kills
// inside a step of store/sql (the intent's temp, each move, the intent's removal) are
// store/sql's TestRebuildCrashPoints. After each kill the intent is there, so a start
// refuses (ErrRebuildInterrupted); the next run of db rebuild finishes with the intent's
// UUID and folders:
//
//	| Killed at                 | Left                                        | Next run                      |
//	|---------------------------|---------------------------------------------|-------------------------------|
//	| rebuild-intent-written    | the intent                                  | moves, keeps, builds, on      |
//	| rebuild-moved-synced      | app.db and its WAL files moved              | keeps the temps, builds, on   |
//	| rebuild-temp-kept (1st)   | save-1 kept, still in the temp folder       | same bytes: removes it, on    |
//	| rebuild-temp-removed (1st)| save-1 kept and removed                     | keeps save-2, on              |
//	| rebuild-temp-kept (2nd)   | save-1 done, save-2 kept, still there       | removes save-2, on            |
//	| rebuild-db-created        | a new app.db without the rebuild's UUID     | moves it to attempt-1/, on    |
//	| rebuild-assigned          | app.db with the UUID, no marker written     | step 5                        |
//	| rebuild-marker-written    | the marker written                          | writes it again, the format   |
//	| rebuild-format-written    | the format written                          | removes the intent            |
func TestRebuildCrashPoints(t *testing.T) {
	both := "save-1,save-2"
	for _, c := range []struct {
		at   string
		want rbLeft
	}{
		{"rebuild-intent-written", rbLeft{appDB: true, inVault: both}},
		{"rebuild-moved-synced", rbLeft{moved: true, inVault: both}},
		{"rebuild-temp-kept:1", rbLeft{moved: true, kept: "save-1", inVault: both}},
		{"rebuild-temp-removed:1", rbLeft{moved: true, kept: "save-1", inVault: "save-2"}},
		{"rebuild-temp-kept:2", rbLeft{moved: true, kept: both, inVault: "save-2"}},
		{"rebuild-db-created", rbLeft{moved: true, kept: both, appDB: true}},
		{"rebuild-assigned", rbLeft{moved: true, kept: both, appDB: true}},
		{"rebuild-marker-written", rbLeft{moved: true, kept: both, appDB: true, marked: true}},
		{"rebuild-format-written", rbLeft{moved: true, kept: both, appDB: true, marked: true}},
	} {
		at := c.at
		t.Run(at, func(t *testing.T) {
			e := rebuildEnv(t)
			killRebuildAt(t, e, at)
			in, err := sqlstore.ReadRebuildIntent(e.state)
			if err != nil || in == nil {
				t.Fatalf("after the kill: intent %v, %v", in, err)
			}
			if got := readRBLeft(t, e, *in); got != c.want {
				t.Fatalf("after the kill %+v, want %+v", got, c.want)
			}
			if _, err := sqlstore.Open(ctx, e.state, slog.New(slog.DiscardHandler)); !errors.Is(err, sqlstore.ErrRebuildInterrupted) {
				t.Fatalf("a start during the rebuild: %v, want ErrRebuildInterrupted", err)
			}
			r, err := Rebuild(ctx, rebuildOpts(e))
			must(t, err)
			if !r.Resumed || r.UUID != in.UUID {
				t.Fatalf("the next run: %+v, want the intent's rebuild %s resumed", r, in.UUID)
			}
			checkRebuilt(t, e, in.UUID)
			_, err = os.Stat(filepath.Join(e.state, in.DamagedDir(), "attempt-1", sqlstore.AppDB))
			if wantAttempt := at == "rebuild-db-created"; (err == nil) != wantAttempt {
				t.Fatalf("attempt-1/app.db there = %v, want %v", err == nil, wantAttempt)
			}
		})
	}
}

// rbLeft is what a kill left: the original moved, the temps kept in recovery/ and still in
// the vault, an app.db in the state directory (the original before step 2, a new one after),
// and the marker naming a vault.
type rbLeft struct {
	moved, appDB, marked bool
	kept, inVault        string
}

func readRBLeft(t *testing.T, e *env, in sqlstore.Rebuild) rbLeft {
	t.Helper()
	names := func(dir string) string {
		var out []string
		for _, n := range []string{"save-1", "save-2"} {
			if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
				out = append(out, n)
			}
		}
		return strings.Join(out, ",")
	}
	_, moved := os.Stat(filepath.Join(e.state, in.DamagedDir(), sqlstore.AppDB))
	_, db := os.Stat(filepath.Join(e.state, sqlstore.AppDB))
	m, _ := vault.ReadMarker(e.dir)
	return rbLeft{moved: moved == nil, appDB: db == nil, marked: m.VaultUUID != "",
		kept: names(filepath.Join(e.state, in.RecoveryDir())), inVault: names(filepath.Join(e.dir, vault.TmpDir))}
}

// TestRebuildCrashWithAStaleWAL: app.db lost, its -wal and -shm left (had_db false). They
// move first; a kill once the new database exists leaves it with its own -wal and -shm, and
// the next run moves those to attempt-1/, never into the damaged folder beside the stale
// ones (it used to stop there, with EEXIST, on every run).
func TestRebuildCrashWithAStaleWAL(t *testing.T) {
	e := rebuildEnv(t)
	must(t, os.Remove(filepath.Join(e.state, sqlstore.AppDB)))
	for _, n := range []string{"-wal", "-shm"} {
		must(t, os.WriteFile(filepath.Join(e.state, sqlstore.AppDB+n), []byte("stale"), 0o600))
	}
	killRebuildAt(t, e, "rebuild-db-created")
	r, err := Rebuild(ctx, rebuildOpts(e))
	must(t, err)
	if r.HadDB {
		t.Fatal("had_db with no app.db")
	}
	d := filepath.Join(e.state, r.DamagedDir())
	for _, p := range []string{"app.db-wal", "app.db-shm", "attempt-1/app.db"} {
		if _, err := os.Stat(filepath.Join(d, p)); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(d, "app.db-wal")); err != nil || string(b) != "stale" { //nolint:gosec // G304: the test's state
		t.Fatalf("the stale WAL in the damaged folder: %q, %v", b, err)
	}
	e.open()
	e.row("a.md")
}

// TestRebuildCrashTwiceKeepsBothAttempts: two half-built databases go to attempt-1 and
// attempt-2, never over each other or the original.
func TestRebuildCrashTwiceKeepsBothAttempts(t *testing.T) {
	e := rebuildEnv(t)
	killRebuildAt(t, e, "rebuild-db-created")
	killRebuildAt(t, e, "rebuild-db-created")
	r, err := Rebuild(ctx, rebuildOpts(e))
	must(t, err)
	checkRebuilt(t, e, r.UUID)
	for _, a := range []string{"attempt-1", "attempt-2"} {
		if _, err := os.Stat(filepath.Join(e.state, r.DamagedDir(), a, sqlstore.AppDB)); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
}
