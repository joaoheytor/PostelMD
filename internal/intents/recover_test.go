//go:build linux

package intents

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// TestInterferenceIsFlaggedAndAudited: a flagged intent stays, its sides are copied, and the
// error log and the audit event name its op ID and kind, never a path.
func TestInterferenceIsFlaggedAndAudited(t *testing.T) {
	e := newEnv(t)
	e.saveIntent()
	must(t, os.Remove(filepath.Join(e.dir, "notes", "a.md")))
	e.write("notes/a.md", "other")
	must(t, os.Link(filepath.Join(e.dir, "notes", "a.md"), filepath.Join(e.dir, e.temp())))
	if tot := e.recover(); tot.Flagged != 1 {
		t.Fatalf("totals %+v, want one flagged", tot)
	}
	logs := e.logs.String()
	for _, want := range []string{`"level":"ERROR"`, `"event":"intent.flagged"`, `"op_id":"` + opA + `"`, `"kind":"save"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("the logs lack %s:\n%s", want, logs)
		}
	}
	// A second start leaves the flagged intent and its copies as they are.
	e.logs.Reset()
	if tot := e.recover(); tot != (Totals{}) {
		t.Fatalf("a second recovery did %+v; want nothing", tot)
	}
	if in, ok := e.intent(); !ok || !in.Flagged {
		t.Fatal("the flagged intent is gone")
	}
}

// TestAKindWithNoRecoveryYetIsFlagged: a kind this build has no recovery for yet is
// interference: never guessed at. (A payload that doesn't decode takes the same path; the
// store refuses to write one, so only store/sql's tests can make it.)
func TestAKindWithNoRecoveryYetIsFlagged(t *testing.T) {
	e := newEnv(t)
	sum := sha256.Sum256([]byte("new"))
	must(t, e.db.Begin(ctx, opA, sqlstore.CreateIntent{Path: "notes/b.md", NewSHA256: sum[:]}, t0))
	e.write(e.temp(), "new")
	e.recover()
	if in, ok := e.intent(); !ok || !in.Flagged || e.copies()["temp"] != "new" {
		t.Fatalf("intent %+v, %v, copies %v; want it flagged, its temp copied", in, ok, e.copies())
	}
}

// TestARollForwardOnAChangedRowIsFlagged: the row isn't at the intent's base (only a writer
// ignoring the instance lock could do that): ErrIntentStale, so the intent is flagged and
// nothing is rolled forward.
func TestARollForwardOnAChangedRowIsFlagged(t *testing.T) {
	e := newEnv(t)
	r := e.saveIntent()
	_, err := e.db.ApplyScan(ctx, t0, [][]sqlstore.Change{{sqlstore.UpdateFile{Row: sqlstore.Row{ID: r.ID, Path: r.Path,
		Generation: r.Generation}, NewPath: r.Path, SHA256: r.SHA256, Stat: r.Stat, Bump: true}}})
	must(t, err)
	must(t, os.Remove(filepath.Join(e.dir, "notes", "a.md")))
	e.write("notes/a.md", "new")
	e.recover()
	if in, ok := e.intent(); !ok || !in.Flagged {
		t.Fatalf("intent %+v, %v; want it flagged", in, ok)
	}
	if got := e.row(); got.Generation != r.Generation+1 || string(got.SHA256) != string(r.SHA256) {
		t.Fatalf("row %+v; want it as the outside bump left it", got)
	}
}

// TestARecoveryErrorNamesTheIntent: an intent recovery can't end (here, its copy can't be
// made) stops the start with an error that names the intent's kind and op ID, so the
// operator can find it, and no path.
func TestARecoveryErrorNamesTheIntent(t *testing.T) {
	e := newEnv(t)
	e.saveIntent()
	e.write("notes/a.md", "other")
	e.write(e.temp(), "new")
	must(t, os.WriteFile(filepath.Join(e.state, "recovery"), nil, 0o600)) // not a folder
	_, err := e.tryRecover()
	if err == nil || !strings.Contains(err.Error(), "intent recovery: the save intent "+opA+":") {
		t.Fatalf("Recover = %v; want an error naming the save intent %s", err, opA)
	}
	if strings.Contains(err.Error(), "rebuild") {
		t.Fatalf("Recover = %v; want no word of db rebuild's own interference", err)
	}
	if in, open := e.intent(); !open || in.Flagged {
		t.Fatalf("intent %+v, %v; want it still open, for the next start", in, open)
	}
}

// TestRemainingNamesWhatTheSweepSpares: after recovery, the temps of the intents left (the
// flagged ones) are spared; nothing else is.
func TestRemainingNamesWhatTheSweepSpares(t *testing.T) {
	e := newEnv(t)
	e.saveIntent()
	must(t, e.db.Flag(ctx, opA))
	spare, err := Remaining(ctx, e.db)
	must(t, err)
	if !spare(opA) || spare("ffffffffffffffffffffffffffffffff") {
		t.Fatal("Remaining spares the wrong temps")
	}
}
