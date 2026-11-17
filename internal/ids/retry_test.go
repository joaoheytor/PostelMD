//go:build linux

package ids

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// A read of a due hold runs a full pass; while full passes keep failing (a folder the walk
// can't read), reads don't walk the vault again until FullPassRetry has passed since the
// last failure, and are served meanwhile. Once the walk works, a read resolves the hold
// (#184, ADR-0037).
func TestAFailedFullPassIsNotRetriedByEveryRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	e := newEnv(t, map[string]string{"a.md": "unique", "locked/b.md": "B"})
	id := e.row("a.md").ID
	e.aside("a.md")
	e.pass() // a.md suspect
	e.cp2("unique", "copy.md")
	e.pass() // copy.md held for a.md
	if h := e.holds(); len(h) != 1 {
		t.Fatalf("holds %+v, want copy.md held", h)
	}
	walks := 0
	setKillPoint(t, func(p string) {
		if p == "scan-walk" {
			walks++
		}
	})
	locked := filepath.Join(e.dir, "locked")
	must(t, os.Chmod(locked, 0o300))                  //nolint:gosec // G302: a folder without its read bit, under test
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // G302: a folder, restored so TempDir can remove it
	e.after(SecondPass)

	if _, err := e.s.Seen(ctx, "copy.md"); err == nil || walks != 1 {
		t.Fatalf("the first read of the due hold: %v after %d walks; want the full pass's error after one", err, walks)
	}
	for range 3 {
		if wait, err := e.s.Seen(ctx, "copy.md"); err != nil || wait != 0 {
			t.Fatalf("a read inside the retry delay = %v, %v; want it served", wait, err)
		}
	}
	e.after(FullPassRetry - 1)
	if _, err := e.s.Seen(ctx, "copy.md"); err != nil || walks != 1 {
		t.Fatalf("reads inside the retry delay walked %d times (%v), want 1", walks, err)
	}
	e.after(1)
	if _, err := e.s.Seen(ctx, "copy.md"); err == nil || walks != 2 {
		t.Fatalf("after the retry delay: %v after %d walks; want a second failed walk", err, walks)
	}
	must(t, os.Chmod(locked, 0o700)) //nolint:gosec // G302: a folder, readable again
	e.after(FullPassRetry)
	if wait, err := e.s.Seen(ctx, "copy.md"); err != nil || wait != 0 || walks != 3 {
		t.Fatalf("once the walk works: %v, %v after %d walks", wait, err, walks)
	}
	if r := e.row("copy.md"); r.ID != id {
		t.Fatalf("copy.md: %+v, want a.md's ID", r)
	}
}

// A full pass that succeeds clears the failure: the next due hold's read runs one at once.
func TestASuccessfulPassClearsTheRetryDelay(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	e.s.fullFailed(e.clock)
	if !e.s.fullRetryWaits() {
		t.Fatal("a failure just now doesn't hold the reads' full passes back")
	}
	e.pass()
	if e.s.fullRetryWaits() {
		t.Fatal("a successful pass didn't clear the failure")
	}
}

// The retry delay counts from when the pass failed, not from when it started: a walk that
// took 30 s and failed holds the reads back until 60 s after its failure.
func TestTheRetryDelayCountsFromTheFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	e := newEnv(t, map[string]string{"locked/a.md": "A"})
	locked := filepath.Join(e.dir, "locked")
	must(t, os.Chmod(locked, 0o300))                  //nolint:gosec // G302: a folder without its read bit, under test
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // G302: a folder, restored so TempDir can remove it
	setKillPoint(t, func(p string) {
		if p == "scan-walk" {
			e.after(30 * time.Second) // a slow walk
		}
	})
	if _, err := e.s.Pass(ctx); err == nil {
		t.Fatal("a pass over an unreadable folder succeeded")
	}
	e.after(FullPassRetry - 29*time.Second) // 61 s after the start, 31 s after the failure
	if !e.s.fullRetryWaits() {
		t.Fatal("the retry delay was counted from the pass's start")
	}
}

// A failure time in the future (the clock stepped back) counts as now: the wait is never
// longer than FullPassRetry.
func TestAFailureInTheFutureCountsAsNow(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	e.s.fullFailed(e.clock.Add(time.Hour))
	if !e.s.fullRetryWaits() {
		t.Fatal("a failure in the future doesn't hold the reads back now")
	}
	e.after(FullPassRetry)
	if e.s.fullRetryWaits() {
		t.Fatal("a failure in the future held the reads back past FullPassRetry")
	}
}

// A canceled pass (the server stopping) isn't a failure.
func TestACanceledPassRecordsNoFailure(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.s.Pass(canceled); err == nil {
		t.Fatal("a canceled pass succeeded")
	}
	if e.s.fullRetryWaits() {
		t.Fatal("a canceled pass held the reads' full passes back")
	}
}

// A read-only vault (ADR-0037) isn't walked: the pass can't commit, so it returns
// ErrReadOnly first, and that isn't a walk failure.
func TestAPassOnAReadOnlyVaultDoesNotWalk(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	walks := 0
	setKillPoint(t, func(p string) {
		if p == "scan-walk" {
			walks++
		}
	})
	e.v.SetReadOnly()
	if _, err := e.s.Pass(ctx); !errors.Is(err, vault.ErrReadOnly) || walks != 0 {
		t.Fatalf("Pass on a read-only vault: %v after %d walks; want ErrReadOnly, no walk", err, walks)
	}
	if e.s.fullRetryWaits() {
		t.Fatal("a read-only vault was recorded as a failed walk")
	}
}

// The tombstone expiry logs a writer fault once, not every hour (ADR-0037): the database
// refuses every write after it until a restart.
func TestTheExpiryLogsAWriterFaultOnce(t *testing.T) {
	var logs bytes.Buffer
	s := &Scanner{log: slog.New(slog.NewJSONHandler(&logs, nil))}
	fault := errors.Join(sqlstore.ErrWriterFault, errors.New("disk I/O error"))
	logged := false
	for range 3 {
		s.expireFailed(fault, &logged)
	}
	if n := strings.Count(logs.String(), "\n"); n != 1 {
		t.Fatalf("%d records for three faulted runs, want 1: %s", n, logs.String())
	}
	logs.Reset()
	s.expireFailed(errors.New("busy"), &logged)
	s.expireFailed(errors.New("busy"), &logged)
	if n := strings.Count(logs.String(), "\n"); n != 2 {
		t.Fatalf("%d records for two other failures, want one each: %s", n, logs.String())
	}
}
