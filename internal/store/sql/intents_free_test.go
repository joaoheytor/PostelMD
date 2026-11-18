//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// The destinations of a create, a move and a restore, and the holds under a moved or
// deleted item (ADR-0033, "Giving a path a row"). A row the database still has on a path
// the no-clobber rename proved absent is vacated, never tombstoned: it stays suspect, off
// the path, so the scan's rules 1 and 2 and its two-pass absence (ADR-0031) still decide its
// ID. A hold on such a path is released.

// assertDisplaced fails unless the row f is still there, suspect, off its path, and not
// tombstoned, with its hash, stat and generation kept.
func assertDisplaced(t *testing.T, db *DB, f File) {
	t.Helper()
	ctx := context.Background()
	got, err := db.FileByID(ctx, f.ID)
	if err != nil {
		t.Fatalf("the row on the destination is gone (%v); it must stay for the scan's rules", err)
	}
	if !got.Suspect || got.Path == f.Path || got.Generation != f.Generation || string(got.SHA256) != string(f.SHA256) || got.Stat != f.Stat {
		t.Fatalf("the row on the destination: %+v, was %+v; want it suspect, off the path, otherwise unchanged", got, f)
	}
	if _, err := db.TombstoneByID(ctx, f.ID); !errors.Is(err, ErrNoTombstone) {
		t.Fatalf("the row on the destination was tombstoned (%v)", err)
	}
	// The completion's own row may be on the path now; it must not be f.
	if g, err := db.FileByPath(ctx, f.Path); err == nil && g.ID == f.ID {
		t.Fatal("the displaced row still holds its path")
	}
}

func holdCount(t *testing.T, db *DB) int {
	t.Helper()
	h, err := db.Holds(context.Background())
	must(t, err)
	return len(h)
}

// holdOn makes b suspect and holds path for it (a rule-2 match the scan left pending).
func holdOn(t *testing.T, db *DB, b File, path string) {
	t.Helper()
	_, err := db.ApplyScan(context.Background(), t0, [][]Change{{Suspect{Row: rowOf(b)},
		Hold{Path: path, FileID: b.ID, SHA256: b.SHA256, Stat: FileStat{Size: 2, Inode: 99}}}})
	must(t, err)
	if holdCount(t, db) != 1 {
		t.Fatal("the hold wasn't recorded")
	}
}

// staleRow gives path a row whose file isn't there: a second stale row under a destination,
// so two rows are vacated in one completion.
func staleRow(t *testing.T, db *DB, path string) File {
	t.Helper()
	ctx := context.Background()
	_, err := db.ApplyScan(ctx, t0, [][]Change{{InsertFile{Path: path, SHA256: sum(7), Stat: FileStat{Size: 7, Inode: 70}}}})
	must(t, err)
	f, err := db.FileByPath(ctx, path)
	must(t, err)
	return f
}

func TestCreateOntoAStaleRowDisplacesIt(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	a := rows["a.md"] // live: its file was renamed outside, and no scan has run since
	must(t, db.Begin(ctx, opA, CreateIntent{Path: "a.md", NewSHA256: sum(5)}, t0))
	id, err := db.CompleteCreate(ctx, opA, FileStat{Size: 5, Inode: 50}, t0)
	must(t, err)
	assertDisplaced(t, db, a)
	if got, err := db.FileByPath(ctx, "a.md"); err != nil || got.ID != id {
		t.Fatalf("a.md: %+v, %v; want the created row", got, err)
	}
}

func TestCreateOntoAHeldPathReleasesTheHold(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	b := rows["notes/b.md"]
	holdOn(t, db, b, "c.md")
	must(t, db.Begin(ctx, opA, CreateIntent{Path: "c.md", NewSHA256: sum(5)}, t0))
	_, err := db.CompleteCreate(ctx, opA, FileStat{Size: 5, Inode: 50}, t0)
	must(t, err)
	if holdCount(t, db) != 0 {
		t.Fatal("the hold on the created path is still there")
	}
	if got, err := db.FileByID(ctx, b.ID); err != nil || !got.Suspect || got.Path != b.Path {
		t.Fatalf("the hold's suspect: %+v, %v; want it as it was", got, err)
	}
}

func TestRestoreOntoAStaleRowAndAHold(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	a, b := rows["a.md"], rows["notes/b.md"]
	holdOn(t, db, b, "drafts/held.md")
	folder := anAssignment(t0).Trash[1].OpID
	must(t, db.BeginRestore(ctx, opA, RestoreIntent{TrashOpID: folder, Entry: "e", To: "drafts", Folder: true, Mechanism: MechanismNoReplace}, t0))
	// The restored folder lands on drafts/; a stale row can only be under it, so a.md's row
	// is first moved there by a scan-like update.
	_, err := db.ApplyScan(ctx, t0, [][]Change{{UpdateFile{Row: rowOf(a), NewPath: "drafts/a.md", SHA256: a.SHA256, Stat: a.Stat}}})
	must(t, err)
	a.Path = "drafts/a.md"
	y := staleRow(t, db, "drafts/sub/y.md")
	must(t, db.CompleteRestore(ctx, opA, []NewFile{{Path: "drafts/a.md", SHA256: sum(4)}, {Path: "drafts/held.md", SHA256: sum(6)}}, t0))
	assertDisplaced(t, db, a)
	assertDisplaced(t, db, y)
	if holdCount(t, db) != 0 {
		t.Fatal("the hold on a restored path is still there")
	}
}

func TestCompleteRestoreRefusesARecordThatIsntRestoring(t *testing.T) {
	db, _ := assigned(t)
	ctx := context.Background()
	folder := anAssignment(t0).Trash[1].OpID
	must(t, db.BeginRestore(ctx, opA, RestoreIntent{TrashOpID: folder, Entry: "e", To: "drafts", Folder: true, Mechanism: MechanismNoReplace}, t0))
	// Only a writer ignoring the instance lock could move the record meanwhile.
	must(t, db.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE trash_records SET state = 'purging' WHERE op_id = ?", folder)
		return err
	}))
	if err := db.CompleteRestore(ctx, opA, []NewFile{{Path: "drafts/x.md", SHA256: sum(4)}}, t0); !errors.Is(err, ErrTrashState) {
		t.Fatalf("CompleteRestore of a purging record = %v, want ErrTrashState", err)
	}
	if _, err := db.FileByPath(ctx, "drafts/x.md"); !errors.Is(err, ErrNoFile) {
		t.Fatal("the refused restore still gave a new ID")
	}
	if onlyIntent(t, db).OpID != opA {
		t.Fatal("the refused restore ended its intent")
	}
}

func TestDeleteReleasesTheHoldsUnderTheItem(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	holdOn(t, db, rows["a.md"], "notes/held.md")
	p := folderPayload(ctx, db)
	must(t, db.BeginDelete(ctx, stamp, p, folderItem, t0))
	must(t, db.CompleteDelete(ctx, stamp, t0))
	if holdCount(t, db) != 0 {
		t.Fatal("the hold under the deleted folder is still there")
	}
}

// TestMoveOntoStaleRowsAndHolds: a file moved onto a path with a stale row, and a folder
// moved onto a folder whose paths have a stale row and a hold. The stale rows are displaced
// before any moved row takes its path (files.path is unique), and the holds released.
func TestMoveOntoStaleRowsAndHolds(t *testing.T) {
	t.Run("a file", func(t *testing.T) {
		db, rows := assigned(t)
		ctx := context.Background()
		a, b := rows["a.md"], rows["notes/b.md"]
		p := MoveIntent{From: "notes/b.md", To: "a.md", Device: 2049, Inode: b.Stat.Inode, Files: []Affected{affected(b)}, Mechanism: MechanismNoReplace}
		must(t, db.Begin(ctx, opA, p, t0))
		must(t, db.CompleteMove(ctx, opA, nil, t0))
		assertDisplaced(t, db, a)
		if got, err := db.FileByPath(ctx, "a.md"); err != nil || got.ID != b.ID || got.Generation != b.Generation+1 {
			t.Fatalf("a.md: %+v, %v; want b.md's row, bumped", got, err)
		}
	})
	t.Run("a folder", func(t *testing.T) {
		db, rows := assigned(t)
		ctx := context.Background()
		a, b := rows["a.md"], rows["notes/b.md"]
		// a.md's file went to archive/b.md's path outside; archive/held.md is held for a.md.
		_, err := db.ApplyScan(ctx, t0, [][]Change{{UpdateFile{Row: rowOf(a), NewPath: "archive/b.md", SHA256: a.SHA256, Stat: a.Stat}}})
		must(t, err)
		a.Path = "archive/b.md"
		_, err = db.ApplyScan(ctx, t0, [][]Change{{InsertFile{Path: "x.md", SHA256: sum(8), Stat: FileStat{Inode: 80}}}})
		must(t, err)
		x, err := db.FileByPath(ctx, "x.md")
		must(t, err)
		holdOn(t, db, x, "archive/held.md")
		y := staleRow(t, db, "archive/y.md")
		must(t, db.Begin(ctx, opA, moveFolder(ctx, db), t0))
		must(t, db.CompleteMove(ctx, opA, nil, t0))
		assertDisplaced(t, db, a)
		assertDisplaced(t, db, y)
		if got, err := db.FileByPath(ctx, "archive/b.md"); err != nil || got.ID != b.ID {
			t.Fatalf("archive/b.md: %+v, %v; want notes/b.md's row", got, err)
		}
		if holdCount(t, db) != 0 {
			t.Fatal("the hold on the destination is still there")
		}
	})
}

// TestADisplacedRowIsTombstonedUnderItsPath: if the scan's second pass still misses a
// displaced row's file, its tombstone records the path it last had, not the placeholder.
func TestADisplacedRowIsTombstonedUnderItsPath(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	a := rows["a.md"]
	must(t, db.Begin(ctx, opA, CreateIntent{Path: "a.md", NewSHA256: sum(5)}, t0))
	_, err := db.CompleteCreate(ctx, opA, FileStat{Size: 5, Inode: 50}, t0)
	must(t, err)
	d, err := db.FileByID(ctx, a.ID)
	must(t, err)
	_, err = db.ApplyScan(ctx, t0, [][]Change{{TombstoneFile{Row: rowOf(d), Reason: "missing"}}})
	must(t, err)
	if ts, err := db.TombstoneByID(ctx, a.ID); err != nil || ts.LastPath != "a.md" {
		t.Fatalf("the tombstone: %+v, %v; want a.md as its last path", ts, err)
	}
}
