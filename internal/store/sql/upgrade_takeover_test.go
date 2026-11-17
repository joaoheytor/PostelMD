//go:build linux

package sql

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// noMarker is StuckUpgrade's marker check for a marker that is empty, or names the intent's
// vault.
func noMarker(string) (bool, error) { return false, nil }

// TestStuckUpgrade: db rebuild takes over a first upgrade only for a positive reason it can't
// finish (ADR-0041): an intent PostelMD didn't write, a database with file IDs under another
// vault UUID, a damaged database, or a marker naming another vault (or not parsing). A
// resumable upgrade gives no reason; an I/O error is an error, never a reason.
func TestStuckUpgrade(t *testing.T) {
	ctx := context.Background()
	intent := func(t *testing.T, dir string) Upgrade {
		u, err := BeginUpgrade(dir, rebuildNow)
		must(t, err)
		return u
	}
	for _, c := range []struct {
		name   string
		setup  func(t *testing.T, dir string)
		marker func(string) (bool, error)
		stuck  bool
	}{
		{"no intent", func(*testing.T, string) {}, noMarker, false},
		{"resumable, no IDs yet", func(t *testing.T, dir string) { intent(t, dir) }, noMarker, false},
		{"resumable, IDs under the intent's UUID", func(t *testing.T, dir string) {
			assignIn(t, dir, intent(t, dir).VaultUUID)
		}, noMarker, false},
		{"resumable, no app.db", func(t *testing.T, dir string) {
			intent(t, dir)
			mustRemove(t, filepath.Join(dir, AppDB))
		}, noMarker, false},
		{"an intent PostelMD didn't write", func(t *testing.T, dir string) {
			must(t, os.WriteFile(filepath.Join(dir, UpgradeIntent), []byte("{"), 0o600))
		}, noMarker, true},
		{"IDs under another vault UUID", func(t *testing.T, dir string) {
			intent(t, dir)
			assignIn(t, dir, "00000000-0000-4000-8000-000000000000")
		}, noMarker, true},
		{"a damaged app.db", func(t *testing.T, dir string) {
			intent(t, dir)
			must(t, os.WriteFile(filepath.Join(dir, AppDB), []byte("not a database, and long enough to be read as one"), 0o600))
		}, noMarker, true},
		{"the marker names another vault", func(t *testing.T, dir string) { intent(t, dir) },
			func(string) (bool, error) { return true, nil }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := closedDB(t)
			c.setup(t, dir)
			reason, err := StuckUpgrade(ctx, dir, c.marker)
			if err != nil || (reason != "") != c.stuck {
				t.Fatalf("StuckUpgrade = %q, %v; want stuck %v", reason, err, c.stuck)
			}
		})
	}
}

// TestStuckUpgradeErrs: what it can't read is an error, not a reason to take over.
func TestStuckUpgradeErrs(t *testing.T) {
	ctx := context.Background()
	dir := closedDB(t)
	must(t, os.Symlink(filepath.Join(dir, "x"), filepath.Join(dir, UpgradeIntent)))
	if reason, err := StuckUpgrade(ctx, dir, noMarker); err == nil {
		t.Fatalf("a symlinked upgrade.intent: %q, want an error", reason)
	}
	dir = closedDB(t)
	_, err := BeginUpgrade(dir, rebuildNow)
	must(t, err)
	failing := errors.New("the marker couldn't be read")
	if _, err := StuckUpgrade(ctx, dir, func(string) (bool, error) { return false, failing }); !errors.Is(err, failing) {
		t.Fatalf("a marker read error: %v", err)
	}
	if os.Geteuid() != 0 {
		must(t, os.Chmod(filepath.Join(dir, AppDB), 0))
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, AppDB), 0o600) })
		if reason, err := StuckUpgrade(ctx, dir, noMarker); err == nil {
			t.Fatalf("an unreadable app.db: %q, want an error", reason)
		}
	}
}

// TestRebuildTakesOverAnUpgrade: BeginRebuildOverUpgrade lists upgrade.intent with the
// database's files, and the move takes it into the damaged folder; pre-format-2/ stays.
// Without an intent it refuses; plain BeginRebuild still refuses beside one.
func TestRebuildTakesOverAnUpgrade(t *testing.T) {
	ctx := context.Background()
	dir := closedDB(t)
	_, err := BeginUpgrade(dir, rebuildNow)
	must(t, err)
	mustMkdir(t, filepath.Join(dir, "pre-format-2"))
	r, err := BeginRebuildOverUpgrade(ctx, dir, rebuildNow)
	must(t, err)
	if !r.Upgrade || !slices.Contains(r.Moves, UpgradeIntent) {
		t.Fatalf("intent %+v, want the upgrade's intent among its moves", r)
	}
	if got, err := ReadRebuildIntent(dir); err != nil || got == nil || !got.Upgrade {
		t.Fatalf("ReadRebuildIntent = %+v, %v", got, err)
	}
	_, err = MoveAsideForRebuild(ctx, dir, r)
	must(t, err)
	if exists(dir, UpgradeIntent) || !exists(filepath.Join(dir, r.DamagedDir()), UpgradeIntent) || !exists(dir, "pre-format-2") {
		t.Fatal("upgrade.intent didn't move into the damaged folder, or pre-format-2/ moved")
	}

	dir = closedDB(t)
	if _, err := BeginRebuildOverUpgrade(ctx, dir, rebuildNow); !errors.Is(err, ErrRebuildRefused) || exists(dir, RebuildIntent) {
		t.Fatalf("a takeover without upgrade.intent = %v", err)
	}
}

// TestRebuildIntentWithAnUpgradeIsChecked: rebuild.intent lists upgrade.intent only with its
// upgrade flag, and the flag only with it.
func TestRebuildIntentWithAnUpgradeIsChecked(t *testing.T) {
	ok := Rebuild{UUID: NewUUID(), Time: "2026-11-17T10:00:00Z", Upgrade: true, Moves: []string{UpgradeIntent}}
	if !ok.valid() {
		t.Fatalf("%+v isn't valid", ok)
	}
	for _, r := range []Rebuild{
		{UUID: ok.UUID, Time: ok.Time, Moves: []string{UpgradeIntent}},
		{UUID: ok.UUID, Time: ok.Time, Upgrade: true},
	} {
		if r.valid() {
			t.Fatalf("%+v is valid", r)
		}
	}
}

// The takeover's crash points (ADR-0041): upgrade.intent is one more of the original move's
// files, after app.db, -wal and -shm, so a kill leaves it in the state directory or in the
// damaged folder, never in both or neither, and the next run finishes the move:
//
//	| Killed at              | upgrade.intent           | Next run                   |
//	|------------------------|--------------------------|----------------------------|
//	| rebuild-intent-written | in the state directory   | moves the four files       |
//	| rebuild-moved (3rd)    | in the state directory   | moves it, records the move |
//	| rebuild-moved (4th)    | in the damaged folder    | records the move           |
//	| rebuild-moved-recorded | in the damaged folder    | nothing more to move       |
func TestRebuildTakeoverCrashPoints(t *testing.T) {
	for _, c := range []struct {
		at    string
		moved bool
	}{
		{"rebuild-intent-written:1", false},
		{"rebuild-moved:3", false},
		{"rebuild-moved:4", true},
		{"rebuild-moved-recorded:1", true},
	} {
		t.Run(c.at, func(t *testing.T) {
			dir := walDB(t)
			_, err := BeginUpgrade(dir, rebuildNow)
			must(t, err)
			d := filepath.Join(dir, "damaged-"+rebuildStamp)
			killRebuildSteps(t, dir, "steps", c.at)
			if exists(dir, UpgradeIntent) == c.moved || exists(d, UpgradeIntent) != c.moved {
				t.Fatalf("after the kill: in the state directory %v, damaged %v; want moved %v",
					exists(dir, UpgradeIntent), exists(d, UpgradeIntent), c.moved)
			}
			r, err := rebuildSteps(dir, false)
			must(t, err)
			if !r.Upgrade || exists(dir, UpgradeIntent) || !exists(d, UpgradeIntent) || !exists(d, movedMarker) {
				t.Fatalf("after the next run (%+v): upgrade.intent not moved, or the move not recorded", r)
			}
		})
	}
}

// assignIn sets the "assigned" flag with vaultUUID in dir's app.db.
func assignIn(t *testing.T, dir, vaultUUID string) {
	t.Helper()
	db, err := Open(context.Background(), dir, discard)
	must(t, err)
	must(t, db.Assign(context.Background(), Assignment{VaultUUID: vaultUUID, Now: rebuildNow}))
	must(t, db.Close())
}
