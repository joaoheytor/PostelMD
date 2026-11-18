//go:build linux

package sql

import (
	"context"
	"errors"
	"testing"
)

// TestResolve: the operator's way out of a flagged intent (postelmd intents resolve). The
// intent goes with what began with it, as a drop: a delete's moving record is removed, a
// restore's record goes back to trashed. An open intent is recovery's and is refused, and
// an op ID with no intent is ErrNoIntent; neither changes anything.
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
	must(t, db.Resolve(ctx, opA))
	noIntent(t, db)
	if err := db.Resolve(ctx, opA); !errors.Is(err, ErrNoIntent) {
		t.Fatalf("resolving it again = %v, want ErrNoIntent", err)
	}

	under, err := db.FilesUnder(ctx, "notes")
	must(t, err)
	p := DeleteIntent{Path: "notes", Folder: true, Device: 2049, Inode: 40, Mechanism: MechanismNoReplace}
	for _, f := range under {
		p.Files = append(p.Files, affected(f))
	}
	must(t, db.BeginDelete(ctx, stamp, p, NewTrashItem{Name: "notes", OriginalPath: "notes", Kind: "folder"}, t0))
	must(t, db.Flag(ctx, stamp))
	must(t, db.Resolve(ctx, stamp))
	if s := recordState(t, db, stamp); s != "" {
		t.Fatalf("after resolving a delete, record state %q, want none", s)
	}

	folder := anAssignment(t0).Trash[1].OpID
	must(t, db.BeginRestore(ctx, opB, RestoreIntent{TrashOpID: folder, Entry: BytePath(".postelmd-trash/" + folder),
		To: "drafts", Folder: true, Device: 2049, Inode: 8, Mechanism: MechanismNoReplace}, t0))
	must(t, db.Flag(ctx, opB))
	must(t, db.Resolve(ctx, opB))
	if s := recordState(t, db, folder); s != "trashed" {
		t.Fatalf("after resolving a restore, record %q, want trashed", s)
	}
	noIntent(t, db)
}
