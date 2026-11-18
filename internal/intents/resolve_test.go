//go:build linux

package intents

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/audit"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

func (e *env) resolve() (Resolution, error) { return e.resolveID(opA) }

// resolveID resolves opID through e.wrap, when set.
func (e *env) resolveID(opID string) (Resolution, error) {
	log := e.logger()
	var db Store = e.db
	if e.wrap != nil {
		db = e.wrap(e.db)
	}
	return Resolve(ctx, Recovery{DB: db, Vault: e.v, State: e.state, Log: log, Audit: audit.New(log)}, opID)
}

// flaggedWithTemp leaves a flagged save intent with temp as its temp's bytes and copy as
// recovery/<op-id>/temp's ("" for none).
func flaggedWithTemp(t *testing.T, temp, copy_ string) *env {
	e := newEnv(t)
	e.saveIntent()
	e.write(e.temp(), temp)
	must(t, e.db.Flag(ctx, opA))
	if copy_ != "" {
		dir := filepath.Join(e.state, "recovery", opA)
		must(t, os.MkdirAll(dir, 0o700))
		must(t, os.WriteFile(filepath.Join(dir, "temp"), []byte(copy_), 0o600))
	}
	return e
}

// TestResolveAFlaggedIntent: resolve ends a flagged intent. Its temp goes only once a copy of
// it is in recovery/<op-id>/: an existing copy with the same bytes is that copy; with other
// bytes, or none, the temp is copied first. The copies stay, and an audit event names the
// op ID and kind.
func TestResolveAFlaggedIntent(t *testing.T) {
	for _, c := range []struct {
		name, copy_ string
		want        map[string]string
	}{
		{"its copy is there", "draft", map[string]string{"temp": "draft"}},
		{"no copy yet", "", map[string]string{"temp": "draft"}},
		{"a copy of other bytes", "older", map[string]string{"temp": "older", "temp-1": "draft"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := flaggedWithTemp(t, "draft", c.copy_)
			res, err := e.resolve()
			must(t, err)
			if _, open := e.intent(); open {
				t.Fatal("the intent is still there")
			}
			if e.exists(e.temp()) || !res.TempRemoved || res.Kind != sqlstore.IntentSave {
				t.Fatalf("resolution %+v, temp there %v; want the temp removed", res, e.exists(e.temp()))
			}
			got := e.copies()
			if len(got) != len(c.want) {
				t.Fatalf("copies %v, want %v", got, c.want)
			}
			for n, b := range c.want {
				if got[n] != b {
					t.Fatalf("copies %v, want %v", got, c.want)
				}
			}
			logs := e.logs.String()
			if !strings.Contains(logs, `"event":"intent.resolved"`) || !strings.Contains(logs, opA) || strings.Contains(logs, "a.md") {
				t.Fatalf("the audit record: %s", logs)
			}
		})
	}
}

// TestResolveRefuses: an open intent is recovery's, and an op ID with no intent is an
// error; a copy that can't be made keeps the temp and the intent. None changes anything.
func TestResolveRefuses(t *testing.T) {
	e := newEnv(t)
	e.saveIntent()
	e.write(e.temp(), "draft")
	if _, err := e.resolve(); !errors.Is(err, sqlstore.ErrIntentOpen) {
		t.Fatalf("resolving an open intent = %v, want ErrIntentOpen", err)
	}
	if !e.exists(e.temp()) || len(e.copies()) != 0 {
		t.Fatal("a refused resolve touched the temp")
	}
	must(t, e.db.Flag(ctx, opA))
	must(t, os.WriteFile(filepath.Join(e.state, "recovery"), nil, 0o600)) // not a folder
	if _, err := e.resolve(); err == nil {
		t.Fatal("a resolve whose copy fails: nil")
	}
	if in, open := e.intent(); !open || !in.Flagged || e.read(e.temp()) != "draft" {
		t.Fatal("a failed copy lost the temp or ended the intent")
	}
	must(t, e.db.Resolve(ctx, opA))
	if _, err := e.resolve(); !errors.Is(err, sqlstore.ErrNoIntent) {
		t.Fatalf("resolving no intent = %v, want ErrNoIntent", err)
	}
}

// TestResolveLeavesATempThatIsntAFile: a temp that is a folder or a symlink holds no edit;
// it is left for the next start's sweep, which no intent spares any more.
func TestResolveLeavesATempThatIsntAFile(t *testing.T) {
	e := newEnv(t)
	e.saveIntent()
	must(t, os.Symlink("../notes/a.md", filepath.Join(e.dir, e.temp())))
	must(t, e.db.Flag(ctx, opA))
	res, err := e.resolve()
	must(t, err)
	if _, open := e.intent(); open || res.TempRemoved || !res.TempLeft || len(e.copies()) != 0 {
		t.Fatalf("resolution %+v, copies %v; want the intent ended, the symlink left", res, e.copies())
	}
}

// TestList: every intent with its kind, state, time, and its copies' folder when there is one.
func TestList(t *testing.T) {
	e := flaggedWithTemp(t, "draft", "draft")
	other := "fedcba9876543210fedcba9876543210"
	must(t, e.db.Begin(ctx, other, sqlstore.CreateIntent{Path: "notes/b.md", NewSHA256: make([]byte, 32)}, t0.Add(1)))
	got, err := List(ctx, e.db, e.state)
	must(t, err)
	want := []Listed{
		{OpID: opA, Kind: sqlstore.IntentSave, State: "flagged", At: t0, Copies: filepath.Join(e.state, "recovery", opA)},
		{OpID: other, Kind: sqlstore.IntentCreate, State: "open", At: t0.Add(1)},
	}
	if len(got) != 2 {
		t.Fatalf("List = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].OpID != want[i].OpID || got[i].Kind != want[i].Kind || got[i].State != want[i].State ||
			!got[i].At.Equal(want[i].At.Truncate(1e6)) || got[i].Copies != want[i].Copies {
			t.Fatalf("List[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
