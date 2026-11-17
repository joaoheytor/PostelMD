//go:build linux

package ids

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/layout"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// TestUpgradeCrashHelper runs the first upgrade on -ids.crash-vault and -ids.crash-state and
// is SIGKILLed at -ids.crash-at ("point" or "point:n", the nth time; CLAUDE.md rule 14).
func TestUpgradeCrashHelper(t *testing.T) {
	if *crashAt == "" {
		t.Skip("run by TestUpgradeCrashPoints")
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
	e := openEnv(t, *crashVault, *crashState, rebuildNow)
	u, err := Upgrade(ctx, e.db, e.v, upgradeOpts(e))
	t.Fatalf("not killed at %s (Upgrade: %+v, %v)", *crashAt, u, err)
}

func killUpgradeAt(t *testing.T, e *env, at string) {
	t.Helper()
	out, err := exec.Command(os.Args[0], "-test.run=^TestUpgradeCrashHelper$", "-test.count=1", //nolint:gosec // G204: this test binary, with its own flags
		"-ids.crash-at="+at, "-ids.crash-vault="+e.dir, "-ids.crash-state="+e.state).CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("the helper wasn't killed at %s: %v\n%s", at, err, out)
	}
}

// upLeft is what a kill left: pre-format-2/ (absent, "partial" or "complete"), an incomplete
// copy moved aside, the flag, the marker naming the intent's vault, the format file.
type upLeft struct {
	copy                          string
	aside, flag, marked, formatOK bool
}

func readUpLeft(t *testing.T, e *env, u sqlstore.Upgrade) upLeft {
	t.Helper()
	var l upLeft
	if _, err := os.Stat(filepath.Join(e.state, layout.PreFormat2, "complete")); err == nil {
		l.copy = "complete"
	} else if _, err := os.Stat(filepath.Join(e.state, layout.PreFormat2)); err == nil {
		l.copy = "partial"
	}
	_, err := os.Stat(filepath.Join(e.state, layout.PreFormat2+"-"+u.Stamp(), "stale"))
	l.aside = err == nil
	e.open()
	id, err := e.db.Identity(ctx)
	must(t, err)
	l.flag = id.Assigned && id.VaultUUID == u.VaultUUID
	m, _ := vault.ReadMarker(e.dir)
	l.marked = m.VaultUUID == u.VaultUUID
	_, err = os.Stat(filepath.Join(e.state, "format"))
	l.formatOK = err == nil
	return l
}

// The first upgrade's recovery table (ADR-0041), at the steps internal/ids runs; the kills
// inside the intent's write and removal are store/sql's TestUpgradeIntentCrashPoints. Each
// run starts with an incomplete pre-format-2/ from an earlier attempt, so step 1 moves it
// aside. After each kill the next start finishes the upgrade with the intent's vault UUID:
//
//	| Killed at                | Left                                  | Next start             |
//	|--------------------------|---------------------------------------|------------------------|
//	| upgrade-intent-written   | the intent                            | step 1 on              |
//	| upgrade-copy-moved-aside | the incomplete copy moved aside       | a new copy, on         |
//	| upgrade-copied (1st)     | a partial pre-format-2/               | moves it aside, on     |
//	| upgrade-copy-complete    | pre-format-2/ complete                | keeps it, assigns, on  |
//	| upgrade-assigned         | the flag with the intent's UUID       | marker, format         |
//	| upgrade-marker-written   | the marker                            | marker again, format   |
//	| upgrade-format-written   | the format                            | removes the intent     |
func TestUpgradeCrashPoints(t *testing.T) {
	for _, c := range []struct {
		at   string
		want upLeft
	}{
		{"upgrade-intent-written", upLeft{copy: "partial"}},
		{"upgrade-copy-moved-aside", upLeft{aside: true}},
		{"upgrade-copied:1", upLeft{copy: "partial", aside: true}},
		{"upgrade-copy-complete", upLeft{copy: "complete", aside: true}},
		{"upgrade-assigned", upLeft{copy: "complete", aside: true, flag: true}},
		{"upgrade-marker-written", upLeft{copy: "complete", aside: true, flag: true, marked: true}},
		{"upgrade-format-written", upLeft{copy: "complete", aside: true, flag: true, marked: true, formatOK: true}},
	} {
		t.Run(c.at, func(t *testing.T) {
			e := v01Env(t)
			must(t, os.Mkdir(filepath.Join(e.state, layout.PreFormat2), 0o700))
			must(t, os.WriteFile(filepath.Join(e.state, layout.PreFormat2, "stale"), nil, 0o600))
			e.close()
			killUpgradeAt(t, e, c.at)
			in, err := sqlstore.ReadUpgradeIntent(e.state)
			if err != nil || in == nil {
				t.Fatalf("after the kill: intent %v, %v", in, err)
			}
			if got := readUpLeft(t, e, *in); got != c.want {
				t.Fatalf("after the kill %+v, want %+v", got, c.want)
			}
			u, err := Upgrade(ctx, e.db, e.v, upgradeOpts(e))
			must(t, err)
			if !u.Resumed || u.VaultUUID != in.VaultUUID {
				t.Fatalf("the next start: %+v, want the intent's upgrade %s resumed", u, in.VaultUUID)
			}
			checkUpgraded(t, e, in.VaultUUID)
			if b, err := os.ReadFile(filepath.Join(e.state, layout.PreFormat2, "auth.json")); err != nil || string(b) != "{}" { //nolint:gosec // G304: the test's state
				t.Fatalf("pre-format-2/auth.json = %q, %v", b, err)
			}
		})
	}
}
