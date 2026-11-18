//go:build linux

package intents

import (
	"context"
	"errors"
	"strings"
	"testing"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// kindAs shows every intent as kind: migration 0003's CHECK keeps a kind this build doesn't
// know out of app.db, and a create, move, delete or restore needs a vault set up for it,
// while what resolve decides from the kind is all these tests check. The store's own tests
// (TestResolveRefusesTheKindsItCantEnd) check its refusal on real rows.
type kindAs struct {
	Store
	kind string
}

func (k kindAs) Intents(ctx context.Context) ([]sqlstore.Intent, error) {
	all, err := k.Store.Intents(ctx)
	for i := range all {
		all[i].Kind = k.kind
	}
	return all, err
}

// TestResolveRefusesTheKindsItCantEnd: this build resolves only a save (ADR-0044). A create,
// move, delete or restore began more than a row, and a kind it doesn't know began who knows
// what (ending its row would stop the sweep sparing its temp), so it refuses them before any
// vault I/O: the temp, the copies and the flagged intent stay as they were, and nothing is
// audited.
func TestResolveRefusesTheKindsItCantEnd(t *testing.T) {
	for _, kind := range []string{sqlstore.IntentCreate, sqlstore.IntentMove, sqlstore.IntentDelete, sqlstore.IntentRestore, "rename-tag"} {
		t.Run(kind, func(t *testing.T) {
			e := flaggedWithTemp(t, "draft", "")
			e.wrap = func(s Store) Store { return kindAs{s, kind} }
			if _, err := e.resolve(); !errors.Is(err, sqlstore.ErrResolveKind) {
				t.Fatalf("resolving a flagged %s = %v, want ErrResolveKind", kind, err)
			}
			if in, there := e.intent(); !there || !in.Flagged || e.read(e.temp()) != "draft" || len(e.copies()) != 0 ||
				strings.Contains(e.logs.String(), "intent.resolv") {
				t.Fatalf("a refused resolve changed something: intent %v, copies %v, logs %s", there, e.copies(), e.logs.String())
			}
		})
	}
}

// TestResolveRefusesABadOpID: an op ID no intent can have (a path, a control character) is
// refused before anything is read.
func TestResolveRefusesABadOpID(t *testing.T) {
	e := flaggedWithTemp(t, "draft", "")
	for _, id := range []string{"../" + opA, opA[:8] + "\x1b[2J", ""} {
		if _, err := e.resolveID(id); !errors.Is(err, sqlstore.ErrBadIntent) {
			t.Fatalf("resolving %q = %v, want ErrBadIntent", id, err)
		}
	}
	if in, there := e.intent(); !there || !in.Flagged || e.read(e.temp()) != "draft" {
		t.Fatal("a refused resolve changed the intent or the temp")
	}
}

// failedCommit fails the transaction that ends the intent, as a crash before its commit would.
type failedCommit struct{ Store }

func (failedCommit) Resolve(context.Context, string) error { return errors.New("simulated") }

// TestResolveAudit: intent.resolving is recorded before the transaction that ends the intent,
// and intent.resolved only after it commits (ADR-0044), so a crash between them leaves the
// first without the second.
func TestResolveAudit(t *testing.T) {
	e := flaggedWithTemp(t, "draft", "")
	_, err := e.resolve()
	must(t, err)
	logs := e.logs.String()
	resolving, resolved := strings.Index(logs, `"event":"intent.resolving"`), strings.Index(logs, `"event":"intent.resolved"`)
	if resolving < 0 || resolved < resolving {
		t.Fatalf("audit records out of order: %s", logs)
	}

	e = flaggedWithTemp(t, "draft", "")
	e.wrap = func(s Store) Store { return failedCommit{s} }
	if _, err := e.resolve(); err == nil {
		t.Fatal("a failed commit: nil")
	}
	logs = e.logs.String()
	if !strings.Contains(logs, `"event":"intent.resolving"`) || strings.Contains(logs, `"event":"intent.resolved"`) {
		t.Fatalf("after a failed commit: %s", logs)
	}
	if in, there := e.intent(); !there || !in.Flagged {
		t.Fatal("the intent went with a failed commit")
	}
}
