//go:build linux

package sql

import (
	"context"
	"errors"
	"testing"
)

// recordState returns the trash record opID's state, or "" without one.
func recordState(t *testing.T, db *DB, opID string) string {
	t.Helper()
	recs, err := db.TrashRecords(context.Background())
	must(t, err)
	for _, r := range recs {
		if r.OpID == opID {
			return r.State
		}
	}
	return ""
}

// TestDeleteAndItsRecord: BeginDelete commits the intent and a moving record; Drop removes
// both (not applied); CompleteDelete tombstones every row, sets the record trashed and ends
// the intent.
func TestDeleteAndItsRecord(t *testing.T) {
	db, _ := assigned(t)
	ctx := context.Background()
	under, err := db.FilesUnder(ctx, "notes")
	must(t, err)
	p := DeleteIntent{Path: "notes", Folder: true, Device: 2049, Inode: 40, Mechanism: MechanismNoReplace}
	for _, f := range under {
		p.Files = append(p.Files, affected(f))
	}
	item := NewTrashItem{Name: "notes", OriginalPath: "notes", Kind: "folder"}
	must(t, db.BeginDelete(ctx, stamp, p, item, t0))
	if s := recordState(t, db, stamp); s != "moving" {
		t.Fatalf("record state %q, want moving", s)
	}
	must(t, db.Drop(ctx, stamp))
	if s := recordState(t, db, stamp); s != "" {
		t.Fatalf("after Drop, record state %q, want none", s)
	}
	noIntent(t, db)

	must(t, db.BeginDelete(ctx, stamp, p, item, t0))
	must(t, db.CompleteDelete(ctx, stamp, t0))
	if s := recordState(t, db, stamp); s != "trashed" {
		t.Fatalf("record state %q, want trashed", s)
	}
	for _, f := range under {
		if ts, err := db.TombstoneByID(ctx, f.ID); err != nil || ts.Reason != "trashed" || ts.LastPath != f.Path {
			t.Fatalf("tombstone of %s: %+v, %v", f.Path, ts, err)
		}
	}
	if _, err := db.FileByPath(ctx, "a.md"); err != nil {
		t.Fatal("a row outside the deleted folder went")
	}
	noIntent(t, db)
}

// TestRestoreAndPurgeStates: a restore takes a trashed record by compare-and-set and refuses
// any other state (409); Drop puts it back; CompleteRestore gives new IDs and drops the
// record; a purge takes trashed → purging and refuses a restore meanwhile.
func TestRestoreAndPurgeStates(t *testing.T) {
	db, _ := assigned(t)
	ctx := context.Background()
	imported := anAssignment(t0).Trash
	file, folder := imported[0].OpID, imported[1].OpID
	r := RestoreIntent{TrashOpID: folder, Entry: BytePath(".postelmd-trash/" + folder), To: "drafts", Folder: true,
		Device: 2049, Inode: 8, Mechanism: MechanismNoReplace}
	must(t, db.BeginRestore(ctx, opA, r, t0))
	if s := recordState(t, db, folder); s != "restoring" {
		t.Fatalf("record %q, want restoring", s)
	}
	if err := db.BeginRestore(ctx, opB, r, t0); !errors.Is(err, ErrTrashState) {
		t.Fatalf("a second restore = %v, want ErrTrashState", err)
	}
	if err := db.BeginPurge(ctx, folder); !errors.Is(err, ErrTrashState) {
		t.Fatalf("a purge while restoring = %v, want ErrTrashState", err)
	}
	must(t, db.Drop(ctx, opA))
	if s := recordState(t, db, folder); s != "trashed" {
		t.Fatalf("after Drop, record %q, want trashed", s)
	}

	must(t, db.BeginRestore(ctx, opA, r, t0))
	files := []NewFile{{Path: "drafts/one.md", SHA256: sum(4), Stat: FileStat{Inode: 41}}, {Path: "drafts/two.md", SHA256: sum(5), Stat: FileStat{Inode: 42}}}
	if err := db.CompleteRestore(ctx, opA, []NewFile{{Path: "elsewhere.md"}}, t0); !errors.Is(err, ErrBadIntent) {
		t.Fatalf("restoring a file outside the destination = %v, want ErrBadIntent", err)
	}
	must(t, db.CompleteRestore(ctx, opA, files, t0))
	for _, f := range files {
		if got, err := db.FileByPath(ctx, f.Path); err != nil || got.Generation != 1 {
			t.Fatalf("restored %s: %+v, %v", f.Path, got, err)
		}
	}
	if s := recordState(t, db, folder); s != "" {
		t.Fatalf("after the restore, record %q, want none", s)
	}
	noIntent(t, db)

	must(t, db.BeginPurge(ctx, file))
	purging := RestoreIntent{TrashOpID: file, Entry: "e", To: "old.md", Mechanism: MechanismNoReplace}
	if err := db.BeginRestore(ctx, opB, purging, t0); !errors.Is(err, ErrTrashState) {
		t.Fatalf("a restore while purging = %v, want ErrTrashState", err)
	}
	if err := db.DropTrashRecord(ctx, file, "trashed"); !errors.Is(err, ErrTrashState) {
		t.Fatalf("dropping a purging record as trashed = %v, want ErrTrashState", err)
	}
	must(t, db.DropTrashRecord(ctx, file, "purging"))
	if s := recordState(t, db, file); s != "" {
		t.Fatalf("after the purge, record %q", s)
	}
}
