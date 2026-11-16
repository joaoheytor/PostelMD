//go:build linux

package ids

import (
	"errors"
	"flag"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// The crash helper: a re-run of this test binary that opens the vault and database the
// flags name and runs Start, SIGKILLed at one kill point (CLAUDE.md rule 14).
var (
	crashAt    = flag.String("ids.crash-at", "", "kill point (crash helper only)")
	crashVault = flag.String("ids.crash-vault", "", "vault (crash helper only)")
	crashState = flag.String("ids.crash-state", "", "state directory (crash helper only)")
)

func TestScanCrashHelper(t *testing.T) {
	if *crashAt == "" {
		t.Skip("run by TestScanCrashPoints")
	}
	e := openEnv(t, *crashVault, *crashState, time.Now())
	e.s.now = time.Now
	killPoint = func(p string) {
		if p == *crashAt {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {} // the signal is on its way
		}
	}
	_, err := e.s.Start(ctx)
	t.Fatalf("not killed at %s (Start: %v)", *crashAt, err)
}

// The startup scan's recovery table (ADR-0036). A pass is one transaction, so a kill leaves
// it all or nothing; the suspects' clock is in the database, so the second pass's 10 s are
// counted from the first miss across a restart. The case: a unique file copied, then
// deleted (rule 2), outside PostelMD.
//
//	| Killed at                              | In app.db                         | Next start                         |
//	|----------------------------------------|-----------------------------------|------------------------------------|
//	| scan-planned (the plan made, before    | nothing of the pass               | the first pass plans again: a.md   |
//	|  the vault mutex and the transaction)  |                                   | suspect, copy.md held; the ID moves|
//	|                                        |                                   | 10 s after that pass               |
//	| scan-first-pass-done (committed, the   | a.md suspect since the kill's     | under 10 s since then: still held  |
//	|  second pass not run)                  | pass; copy.md held                | (a read waits); from 10 s, the     |
//	|                                        |                                   | first pass moves the ID            |
//
// The kills inside the transaction (scan-before-commit, scan-after-commit) are
// store/sql's TestApplyScanCrashPoints.
func TestScanCrashPoints(t *testing.T) {
	for _, at := range []string{"scan-planned", "scan-first-pass-done"} {
		t.Run(at, func(t *testing.T) {
			e := newEnv(t, map[string]string{"a.md": "unique", "b.md": "other"})
			id := e.row("a.md").ID
			e.cp("a.md", "copy.md")
			e.rm("a.md")
			e.close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestScanCrashHelper$", "-test.count=1", //nolint:gosec // G204: this test binary, with its own flags
				"-ids.crash-at="+at, "-ids.crash-vault="+e.dir, "-ids.crash-state="+e.state)
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
			}
			killed := time.Now()
			r := openEnv(t, e.dir, e.state, killed)
			if at == "scan-planned" {
				if a := r.row("a.md"); a.Suspect || len(r.holds()) != 0 {
					t.Fatalf("killed before the transaction, but a.md is %+v and holds are %+v", a, r.holds())
				}
				r.pass()
			} else if wait, err := r.s.Seen(ctx, "copy.md"); err != nil || wait <= 0 {
				t.Fatalf("a read just after the kill: Seen = %v, %v; want a wait", wait, err)
			}
			if a := r.row("a.md"); !a.Suspect || len(r.holds()) != 1 {
				t.Fatalf("after the first pass: a.md %+v, holds %+v", a, r.holds())
			}
			r.after(SecondPass)
			if got := r.pass(); got != (Totals{Moves: 1}) {
				t.Fatalf("the pass 10 s later: %+v, want the move", got)
			}
			if c := r.row("copy.md"); c.ID != id || c.Generation != 2 {
				t.Fatalf("copy.md: %+v, want a.md's ID", c)
			}
		})
	}
}
