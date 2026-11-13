//go:build linux

package sql

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestAssignHelper is a re-run of this test binary that opens the database in -sql.crash-dir
// and assigns anAssignment, SIGKILLed at the kill point -sql.crash-at names.
func TestAssignHelper(t *testing.T) {
	if *crashDir == "" || *crashAt == "" {
		t.Skip("run by TestAssignCrashPoints")
	}
	db, err := Open(context.Background(), *crashDir, discard)
	if err != nil {
		t.Fatal(err)
	}
	killPoint = func(p string) {
		if p == *crashAt {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {} // the signal is on its way
		}
	}
	err = db.Assign(context.Background(), anAssignment(time.Now()))
	t.Fatalf("not killed at %s (Assign: %v)", *crashAt, err)
}

// The first assignment is one transaction (ADR-0031), so its recovery table has two rows:
//
//	| Killed at             | On disk                          | Next start                     |
//	|-----------------------|----------------------------------|--------------------------------|
//	| assign-before-commit  | no flag, no UUID, no row         | assigns again (from step 2 of  |
//	|                       |                                  | the upgrade, ADR-0030)         |
//	| assign-after-commit   | the flag, the UUID, every row    | a second Assign is refused;    |
//	|                       |                                  | the upgrade goes on at step 4  |
func TestAssignCrashPoints(t *testing.T) {
	for _, c := range []struct {
		at        string
		committed bool
	}{{"assign-before-commit", false}, {"assign-after-commit", true}} {
		t.Run(c.at, func(t *testing.T) {
			dir := closedDB(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestAssignHelper$", "-test.count=1", //nolint:gosec // G204: this test binary, with its own flags
				"-sql.crash-at="+c.at, "-sql.crash-dir="+dir)
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
			}
			db, err := Open(context.Background(), dir, discard)
			if err != nil {
				t.Fatalf("the next open: %v", err)
			}
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			if !c.committed {
				assertNothingAssigned(t, db)
				if err := db.Assign(ctx, anAssignment(time.Now())); err != nil {
					t.Fatalf("assigning again: %v", err)
				}
				return
			}
			id, err := db.Identity(ctx)
			if err != nil || !id.Assigned || id.VaultUUID != testVaultUUID {
				t.Fatalf("Identity = %+v, %v", id, err)
			}
			for _, f := range anAssignment(time.Now()).Files {
				if _, err := db.FileByPath(ctx, f.Path); err != nil {
					t.Fatalf("FileByPath(%q): %v", f.Path, err)
				}
			}
			if recs, err := db.TrashRecords(ctx); err != nil || len(recs) != 2 {
				t.Fatalf("TrashRecords = %v, %v", recs, err)
			}
			if err := db.Assign(ctx, anAssignment(time.Now())); !errors.Is(err, ErrAssigned) {
				t.Fatalf("a second Assign = %v, want ErrAssigned", err)
			}
		})
	}
}
