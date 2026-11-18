//go:build linux

package sql

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The upgrade runbook (docs/operations/running-v0.1.md) moves the database aside by name:
// its `set --` lines list the files an upgrade leaves. These tests read those lines, so a
// new migration whose files the runbook doesn't name fails here, not in an operator's
// rollback.

const runbook = "../../../docs/operations/running-v0.1.md"

var setLine = regexp.MustCompile("set -- ((?:[^`\\n\\\\]|\\\\\\n)*)")

// runbookSets returns the names on each `set --` line of the runbook that moves database
// files, in order: the move block's first.
func runbookSets(t *testing.T) [][]string {
	t.Helper()
	b, err := os.ReadFile(runbook)
	must(t, err)
	var out [][]string
	for _, m := range setLine.FindAllStringSubmatch(string(b), -1) {
		names := strings.Fields(strings.ReplaceAll(m[1], "\\\n", " "))
		// Only the lines that move database files; other blocks set other arguments.
		if slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, "pre-migrate-") }) {
			out = append(out, names)
		}
	}
	if len(out) < 2 {
		t.Fatalf("found %d set -- lines in the runbook; want the move block's and the rollback's", len(out))
	}
	return out
}

// runbookMove is the move block's names, without the snapshots or the markers a case
// leaves behind.
func runbookMove(t *testing.T, snapshots, markers bool) []string {
	t.Helper()
	var out []string
	for _, name := range runbookSets(t)[0] {
		snap, marker := strings.HasPrefix(name, "pre-migrate-"), strings.HasPrefix(name, "post-migration-started-")
		if (snap && !snapshots) || (marker && !markers) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// TestRunbookNamesEveryUpgradesFiles: every `set --` line names the snapshot and the marker
// of an upgrade from each earlier version to this build's.
func TestRunbookNamesEveryUpgradesFiles(t *testing.T) {
	last := len(embeddedFS(t))
	for i, names := range runbookSets(t) {
		for from := 1; from < last; from++ {
			for _, want := range []string{snapshotName(from, last), markerName(from, last)} {
				if !slices.Contains(names, want) {
					t.Errorf("the runbook's set -- line %d doesn't name %s: %v", i+1, want, names)
				}
			}
		}
	}
}
