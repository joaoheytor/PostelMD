//go:build linux

package sql

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The first upgrade's intent (ADR-0030's data format 2, mechanics and recovery table in
// ADR-0041). internal/ids runs the steps; this is the durable record that one is under way.

// UpgradeIntent is the first upgrade's durable intent in the state directory.
const UpgradeIntent = "upgrade.intent"

var (
	// ErrUpgradeInterference: the upgrade's files don't match its intent (a database assigned
	// under another vault UUID, an intent it didn't write), which only a writer ignoring the
	// instance lock, or a hand edit, can cause. Nothing more is changed; the intent stays.
	ErrUpgradeInterference = errors.New("sql: the first upgrade's files don't match its intent; it " +
		"stopped there and upgrade.intent stays; follow the runbook (ADR-0041)")
	// ErrUpgradeInProgress: an offline command found upgrade.intent and changed nothing.
	ErrUpgradeInProgress = errors.New("sql: a first upgrade to data format 2 is under way (upgrade.intent " +
		"is in the state directory); start the server so it finishes, then run this again (ADR-0041)")
)

// Upgrade is upgrade.intent's content: the vault UUID the upgrade assigns, chosen before its
// first side effect, and its time (UTC, to the second).
type Upgrade struct {
	VaultUUID string `json:"vault_uuid"`
	Time      string `json:"time"`
}

// Stamp is the time as the names of the upgrade's folders carry it (pre-format-2-<stamp>/).
func (u Upgrade) Stamp() string {
	t, _ := time.Parse(time.RFC3339, u.Time) // checked when read or written
	return t.UTC().Format("20060102T150405Z")
}

func (u Upgrade) valid() bool {
	t, err := time.Parse(time.RFC3339, u.Time)
	return uuidPattern.MatchString(u.VaultUUID) && err == nil && t.UTC().Format(time.RFC3339) == u.Time
}

// ReadUpgradeIntent reads upgrade.intent: nil if absent; ErrUpgradeInterference if it isn't
// one BeginUpgrade could have written.
func ReadUpgradeIntent(stateDir string) (*Upgrade, error) {
	if err := checkAbs(stateDir); err != nil {
		return nil, err
	}
	b, err := readSidecar(stateDir, UpgradeIntent, ErrUpgradeInterference)
	if err != nil || b == nil {
		return nil, err
	}
	var u Upgrade
	if err := decodeStrict(b, &u); err != nil || !u.valid() {
		return nil, fmt.Errorf("%w (upgrade.intent doesn't parse)", ErrUpgradeInterference)
	}
	return &u, nil
}

// BeginUpgrade makes upgrade.intent durable with a new vault UUID: the first upgrade's first
// side effect (CLAUDE.md rule 14). The caller holds the instance lock and found no intent
// (ReadUpgradeIntent); one that appeared meanwhile is interference. Beside rebuild.intent it
// refuses (ErrRebuildInterrupted): that rebuild finishes first.
func BeginUpgrade(stateDir string, now time.Time) (Upgrade, error) {
	if err := checkAbs(stateDir); err != nil {
		return Upgrade{}, err
	}
	if _, err := os.Lstat(filepath.Join(stateDir, RebuildIntent)); !errors.Is(err, fs.ErrNotExist) {
		return Upgrade{}, errors.Join(ErrRebuildInterrupted, err)
	}
	u := Upgrade{VaultUUID: NewUUID(), Time: now.UTC().Truncate(time.Second).Format(time.RFC3339)}
	b, err := json.Marshal(u)
	if err != nil {
		return Upgrade{}, err
	}
	err = writeSidecar(stateDir, UpgradeIntent, append(b, '\n'), ErrUpgradeInterference, "upgrade-intent-synced", "upgrade-intent-written")
	return u, err
}

// FinishUpgrade removes upgrade.intent durably: the upgrade's last step.
func FinishUpgrade(stateDir string) error {
	return removeSidecar(stateDir, UpgradeIntent, "upgrade-done")
}

// UpgradeUnderWay refuses an offline command's start (db rebuild, vault claim) while
// upgrade.intent, or anything at its name, is there (ADR-0041).
func UpgradeUnderWay(stateDir string) error {
	if _, err := os.Lstat(filepath.Join(stateDir, UpgradeIntent)); !errors.Is(err, fs.ErrNotExist) {
		return errors.Join(ErrUpgradeInProgress, err)
	}
	return nil
}
