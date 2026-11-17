//go:build linux

package sql

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestUpgradeIntent: BeginUpgrade makes upgrade.intent durable with a new vault UUID and the
// time; ReadUpgradeIntent reads it back; FinishUpgrade removes it.
func TestUpgradeIntent(t *testing.T) {
	dir := t.TempDir()
	if u, err := ReadUpgradeIntent(dir); u != nil || err != nil {
		t.Fatalf("ReadUpgradeIntent on an empty folder = %+v, %v", u, err)
	}
	u, err := BeginUpgrade(dir, rebuildNow)
	must(t, err)
	if !uuidPattern.MatchString(u.VaultUUID) || u.Time != "2026-11-17T10:00:00Z" || u.Stamp() != rebuildStamp {
		t.Fatalf("intent = %+v (stamp %s)", u, u.Stamp())
	}
	got, err := ReadUpgradeIntent(dir)
	if err != nil || got == nil || *got != u {
		t.Fatalf("ReadUpgradeIntent = %+v, %v; want %+v", got, err, u)
	}
	if _, err := BeginUpgrade(dir, rebuildNow); !errors.Is(err, ErrUpgradeInterference) {
		t.Fatalf("a second BeginUpgrade = %v, want ErrUpgradeInterference", err)
	}
	must(t, FinishUpgrade(dir))
	if exists(dir, UpgradeIntent) {
		t.Fatal("FinishUpgrade left the intent")
	}
}

// TestUpgradeIntentRefusesWhatItDidntWrite: anything at its name that isn't a whole intent is
// interference, never parsed into one.
func TestUpgradeIntentRefusesWhatItDidntWrite(t *testing.T) {
	for name, c := range map[string]string{
		"not JSON":      "{",
		"unknown field": `{"vault_uuid":"6f1c2a7e-3b4d-4c5e-8f90-a1b2c3d4e5f6","time":"2026-11-17T10:00:00Z","x":1}`,
		"bad UUID":      `{"vault_uuid":"nope","time":"2026-11-17T10:00:00Z"}`,
		"bad time":      `{"vault_uuid":"6f1c2a7e-3b4d-4c5e-8f90-a1b2c3d4e5f6","time":"yesterday"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			must(t, os.WriteFile(filepath.Join(dir, UpgradeIntent), []byte(c), 0o600))
			if _, err := ReadUpgradeIntent(dir); !errors.Is(err, ErrUpgradeInterference) {
				t.Fatalf("err = %v, want ErrUpgradeInterference", err)
			}
		})
	}
	dir := t.TempDir()
	must(t, os.Symlink(filepath.Join(dir, "x"), filepath.Join(dir, UpgradeIntent)))
	if _, err := ReadUpgradeIntent(dir); !errors.Is(err, ErrUpgradeInterference) {
		t.Fatalf("a symlink: err = %v, want ErrUpgradeInterference", err)
	}
}

// The crash points inside upgrade.intent's write and removal (ADR-0041); the steps between
// them are internal/ids's TestUpgradeCrashPoints:
//
//	| Killed at              | Left                                   | Next run                 |
//	|------------------------|----------------------------------------|--------------------------|
//	| upgrade-intent-synced  | the intent's temp only                 | removes it, writes anew  |
//	| upgrade-intent-written | the intent                             | resumes it               |
//	| upgrade-done           | the intent unlinked, maybe not durably | (no intent) nothing      |
func TestUpgradeIntentCrashPoints(t *testing.T) {
	for _, c := range []struct {
		at          string
		intent, tmp bool
	}{
		{"upgrade-intent-synced:1", false, true},
		{"upgrade-intent-written:1", true, false},
		{"upgrade-done:1", false, false},
	} {
		t.Run(c.at, func(t *testing.T) {
			dir := t.TempDir()
			killRebuildSteps(t, dir, "upgrade", c.at)
			if exists(dir, UpgradeIntent) != c.intent || exists(dir, UpgradeIntent+".tmp") != c.tmp {
				t.Fatalf("after the kill: intent %v, temp %v; want %v, %v", exists(dir, UpgradeIntent),
					exists(dir, UpgradeIntent+".tmp"), c.intent, c.tmp)
			}
			u, err := ReadUpgradeIntent(dir)
			if err != nil || (u != nil) != c.intent {
				t.Fatalf("ReadUpgradeIntent = %+v, %v", u, err)
			}
			if u == nil {
				_, err = BeginUpgrade(dir, rebuildNow)
				must(t, err)
				if exists(dir, UpgradeIntent+".tmp") {
					t.Fatal("the stale temp is left")
				}
			}
		})
	}
}

// TestUpgradeAndRebuildExcludeEachOther: db rebuild refuses to start while a first upgrade is
// under way (ADR-0041), and the upgrade refuses beside a rebuild's intent; neither changes
// anything.
func TestUpgradeAndRebuildExcludeEachOther(t *testing.T) {
	dir := closedDB(t)
	_, err := BeginUpgrade(dir, rebuildNow)
	must(t, err)
	if _, err := BeginRebuild(context.Background(), dir, rebuildNow); !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("BeginRebuild beside upgrade.intent = %v, want ErrUpgradeInProgress", err)
	}
	if exists(dir, RebuildIntent) || !exists(dir, AppDB) {
		t.Fatal("the refused rebuild changed something")
	}

	dir = closedDB(t)
	begin(t, dir)
	if _, err := BeginUpgrade(dir, rebuildNow); !errors.Is(err, ErrRebuildInterrupted) {
		t.Fatalf("BeginUpgrade beside rebuild.intent = %v, want ErrRebuildInterrupted", err)
	}
	if exists(dir, UpgradeIntent) {
		t.Fatal("the refused upgrade wrote its intent")
	}
}
