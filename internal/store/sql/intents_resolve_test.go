//go:build linux

package sql

import (
	"context"
	"errors"
	"testing"
)

// TestResolve: the operator's way out of a flagged intent (postelmd intents resolve,
// ADR-0044). A flagged save's row goes. An open intent is recovery's (ErrIntentOpen), an op ID
// with no intent is ErrNoIntent, and one that isn't 32 hex digits is ErrBadIntent; none
// changes anything.
func TestResolve(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	must(t, db.Begin(ctx, opA, aSave(rows["a.md"]), t0))
	if err := db.Resolve(ctx, opA); !errors.Is(err, ErrIntentOpen) {
		t.Fatalf("resolving an open intent = %v, want ErrIntentOpen", err)
	}
	if in := onlyIntent(t, db); in.Flagged {
		t.Fatal("a refused resolve changed the intent")
	}
	must(t, db.Flag(ctx, opA))
	if err := db.Resolve(ctx, "../x\x1b"); !errors.Is(err, ErrBadIntent) {
		t.Fatalf("resolving a bad op ID = %v, want ErrBadIntent", err)
	}
	must(t, db.Resolve(ctx, opA))
	noIntent(t, db)
	if err := db.Resolve(ctx, opA); !errors.Is(err, ErrNoIntent) {
		t.Fatalf("resolving it again = %v, want ErrNoIntent", err)
	}
}

// TestResolveRefusesTheKindsItCantEnd: create, move, delete and restore began more than a
// row (a trash record, a file on disk), which ending as a drop would orphan or expose to the
// purge (ADR-0044); this build refuses them, changing nothing.
func TestResolveRefusesTheKindsItCantEnd(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	folder := anAssignment(t0).Trash[1].OpID
	begins := map[string]func() error{
		opA: func() error { return db.Begin(ctx, opA, CreateIntent{Path: "new.md", NewSHA256: sum(5)}, t0) },
		opB: func() error {
			return db.Begin(ctx, opB, MoveIntent{From: "a.md", To: "b2.md", Device: 1, Inode: 2,
				Files: []Affected{affected(rows["a.md"])}, Mechanism: MechanismNoReplace}, t0)
		},
		"00000000000000000000000000000001": func() error {
			return db.BeginRestore(ctx, "00000000000000000000000000000001", RestoreIntent{TrashOpID: folder,
				Entry: BytePath(".postelmd-trash/" + folder), To: "drafts", Folder: true, Device: 2049, Inode: 8, Mechanism: MechanismNoReplace}, t0)
		},
	}
	for op, begin := range begins {
		must(t, begin())
		must(t, db.Flag(ctx, op))
		if err := db.Resolve(ctx, op); !errors.Is(err, ErrResolveKind) {
			t.Fatalf("resolving %s = %v, want ErrResolveKind", op, err)
		}
	}
	p := folderPayload(ctx, db)
	must(t, db.BeginDelete(ctx, stamp, p, folderItem, t0))
	must(t, db.Flag(ctx, stamp))
	if err := db.Resolve(ctx, stamp); !errors.Is(err, ErrResolveKind) {
		t.Fatalf("resolving a delete = %v, want a refusal", err)
	}
	ins, err := db.Intents(ctx)
	must(t, err)
	if len(ins) != 4 || recordState(t, db, stamp) != "moving" || recordState(t, db, folder) != "restoring" {
		t.Fatalf("intents %d, records %q %q; want all four flagged intents and their records kept", len(ins),
			recordState(t, db, stamp), recordState(t, db, folder))
	}
}

// TestResolvable: this build resolves only a save. The four kinds that begin more than a row
// aren't, and neither is a kind it doesn't know (migration 0003's CHECK keeps one out of its
// table, so internal/intents tests that refusal through a wrapped store).
func TestResolvable(t *testing.T) {
	for kind, want := range map[string]bool{IntentSave: true, "rename-tag": false, IntentCreate: false, IntentMove: false,
		IntentDelete: false, IntentRestore: false} {
		if Resolvable(kind) != want {
			t.Errorf("Resolvable(%q) = %v, want %v", kind, !want, want)
		}
	}
}

// TestValidOpID: a save's (or another kind's) op ID is 32 lower-case hex digits; a delete's is
// a trash stamp, a name with no slash.
func TestValidOpID(t *testing.T) {
	for _, c := range []struct {
		kind, id string
		want     bool
	}{{IntentSave, opA, true}, {IntentSave, "0123456789ABCDEF0123456789ABCDEF", false}, {IntentSave, opA[:31], false},
		{IntentSave, opA + "\x1b", false}, {IntentSave, stamp, false}, {"rename-tag", "", false},
		{IntentDelete, stamp, true}, {IntentDelete, "../x", false}, {IntentDelete, "", false},
		{IntentDelete, stamp + "\x1b[2J", false}, {IntentDelete, "a\u0085b", false}} {
		if ValidOpID(c.kind, c.id) != c.want {
			t.Errorf("ValidOpID(%q, %q) = %v, want %v", c.kind, c.id, !c.want, c.want)
		}
	}
}
