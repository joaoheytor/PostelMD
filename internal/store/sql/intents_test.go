//go:build linux

package sql

import (
	"context"
	"errors"
	"testing"
	"time"
)

const (
	opA   = "0123456789abcdef0123456789abcdef"
	opB   = "fedcba9876543210fedcba9876543210"
	stamp = "20261118T100000.000000000Z"
)

var t0 = time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)

func aSave(f File) SaveIntent {
	return SaveIntent{FileID: f.ID, Path: BytePath(f.Path), BaseGeneration: f.Generation, BaseSHA256: f.SHA256,
		NewSHA256: sum(9), Stat: f.Stat}
}

func affected(f File) Affected {
	return Affected{ID: f.ID, Generation: f.Generation, Path: BytePath(f.Path)}
}

// onlyIntent returns the one intent, failing unless there is exactly one.
func onlyIntent(t *testing.T, db *DB) Intent {
	t.Helper()
	ins, err := db.Intents(context.Background())
	if err != nil || len(ins) != 1 {
		t.Fatalf("Intents = %+v, %v; want one", ins, err)
	}
	return ins[0]
}

func noIntent(t *testing.T, db *DB) {
	t.Helper()
	if ins, err := db.Intents(context.Background()); err != nil || len(ins) != 0 {
		t.Fatalf("Intents = %+v, %v; want none", ins, err)
	}
}

// TestIntentPayloadsRoundTrip: what Begin writes, Intents and Decode give back, a path that
// isn't UTF-8 included; a payload read as another kind is refused.
func TestIntentPayloadsRoundTrip(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	f := rows["notes/b.md"]
	f.Path = "notes/\xff.md"
	p := aSave(f)
	must(t, db.Begin(ctx, opA, p, t0))
	in := onlyIntent(t, db)
	if in.OpID != opA || in.Kind != IntentSave || in.Flagged || !in.At.Equal(t0) {
		t.Fatalf("intent = %+v", in)
	}
	var got SaveIntent
	must(t, in.Decode(&got))
	if got.Path != p.Path || got.FileID != p.FileID || got.Stat != p.Stat || string(got.NewSHA256) != string(p.NewSHA256) {
		t.Fatalf("decoded %+v, want %+v", got, p)
	}
	if err := in.Decode(&CreateIntent{}); !errors.Is(err, ErrBadIntent) {
		t.Fatalf("a save read as a create = %v, want ErrBadIntent", err)
	}
}

func TestBeginRefusesWhatTheStoreNeverWrites(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	good := aSave(rows["a.md"])
	noHash := good
	noHash.NewSHA256 = nil
	for _, c := range []struct {
		name string
		op   string
		p    Payload
	}{
		{"an op ID that isn't 32 hex digits", "ABC", good},
		{"upper-case hex", "0123456789ABCDEF0123456789ABCDEF", good},
		{"a save without the new hash", opA, noHash},
		{"a create of an absolute path", opA, CreateIntent{Path: "/etc/x", NewSHA256: sum(1)}},
		{"a move into itself", opA, MoveIntent{From: "d", To: "d/e", Folder: true, Mechanism: MechanismNoReplace}},
		{"a move by another mechanism", opA, MoveIntent{From: "a", To: "b", Mechanism: "link-unlink"}},
		{"a move naming a row outside the source", opA, MoveIntent{From: "d", To: "e", Mechanism: MechanismNoReplace,
			Files: []Affected{affected(rows["a.md"])}}},
		{"a delete, which begins with its record", stamp, DeleteIntent{Path: "a.md", Mechanism: MechanismNoReplace}},
		{"a restore, which begins with its record", opA, RestoreIntent{TrashOpID: stamp, Entry: "x", To: "y", Mechanism: MechanismNoReplace}},
	} {
		if err := db.Begin(ctx, c.op, c.p, t0); !errors.Is(err, ErrBadIntent) {
			t.Errorf("%s: Begin = %v, want ErrBadIntent", c.name, err)
		}
	}
	noIntent(t, db)
	must(t, db.Begin(ctx, opA, good, t0))
	if err := db.Begin(ctx, opA, good, t0); err == nil {
		t.Fatal("a second intent with the same op ID was accepted")
	}
}

// TestCompleteSave: the row gets generation + 1, the new hash and stat, and the intent ends,
// together; a row that isn't the intent's base changes nothing.
func TestCompleteSave(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	a := rows["a.md"]
	must(t, db.Begin(ctx, opA, aSave(a), t0))
	st := FileStat{Size: 9, Device: 2049, Inode: 77, MtimeNS: 10, CtimeNS: 11}
	must(t, db.CompleteSave(ctx, opA, st))
	got, err := db.FileByPath(ctx, "a.md")
	must(t, err)
	if got.ID != a.ID || got.Generation != a.Generation+1 || string(got.SHA256) != string(sum(9)) || got.Stat != st {
		t.Fatalf("after the save: %+v", got)
	}
	noIntent(t, db)
	if err := db.CompleteSave(ctx, opA, st); !errors.Is(err, ErrNoIntent) {
		t.Fatalf("completing it again = %v, want ErrNoIntent", err)
	}

	must(t, db.Begin(ctx, opB, aSave(a), t0)) // the base is now one generation behind
	if err := db.CompleteSave(ctx, opB, st); !errors.Is(err, ErrIntentStale) {
		t.Fatalf("CompleteSave on a moved-on row = %v, want ErrIntentStale", err)
	}
	if onlyIntent(t, db).OpID != opB {
		t.Fatal("the stale completion ended the intent")
	}
}

// TestCompleteCreate: a new ID at generation 1; a row left on the path by a file that went
// (a suspect, or a live row the scan hasn't caught up with) is tombstoned as missing, and a
// hold on it released, since the create's no-clobber rename proved the path free.
func TestCompleteCreate(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	b := rows["notes/b.md"]
	_, err := db.ApplyScan(ctx, t0, [][]Change{{Suspect{Row: rowOf(b)}}})
	must(t, err)
	must(t, db.Begin(ctx, opA, CreateIntent{Path: "notes/b.md", NewSHA256: sum(5)}, t0))
	st := FileStat{Size: 5, Inode: 55}
	id, err := db.CompleteCreate(ctx, opA, st, t0)
	must(t, err)
	got, err := db.FileByPath(ctx, "notes/b.md")
	must(t, err)
	if got.ID != id || id == b.ID || got.Generation != 1 || got.Suspect || got.Stat != st {
		t.Fatalf("the created row: %+v (ID %s, old %s)", got, id, b.ID)
	}
	if ts, err := db.TombstoneByID(ctx, b.ID); err != nil || ts.Reason != "missing" {
		t.Fatalf("the old row's tombstone: %+v, %v", ts, err)
	}
	noIntent(t, db)

	// A live row on the destination.
	a := rows["a.md"]
	must(t, db.Begin(ctx, opB, CreateIntent{Path: "a.md", NewSHA256: sum(6)}, t0))
	if _, err := db.CompleteCreate(ctx, opB, st, t0); err != nil {
		t.Fatal(err)
	}
	if ts, err := db.TombstoneByID(ctx, a.ID); err != nil || ts.Reason != "missing" {
		t.Fatalf("the live row's tombstone: %+v, %v", ts, err)
	}
}

// TestCompleteMoveOfAFolder: every row under the folder gets its new path and a generation
// bump with its ID kept; a sibling whose name starts with the folder's isn't under it; holds
// under the source are released.
func TestCompleteMoveOfAFolder(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	_, err := db.ApplyScan(ctx, t0, [][]Change{
		{InsertFile{Path: "notes_x.md", SHA256: sum(3), Stat: FileStat{Inode: 3}}},
		{Suspect{Row: rowOf(rows["a.md"])}, Hold{Path: "notes/held.md", FileID: rows["a.md"].ID, SHA256: sum(1), Stat: FileStat{Inode: 4}}},
	})
	must(t, err)
	under, err := db.FilesUnder(ctx, "notes")
	must(t, err)
	if len(under) != 2 {
		t.Fatalf("FilesUnder(notes) = %+v; want b.md and the unhashed file, not notes_x.md", under)
	}
	p := MoveIntent{From: "notes", To: "archive/notes", Folder: true, Device: 2049, Inode: 40, Mechanism: MechanismNoReplace}
	for _, f := range under {
		p.Files = append(p.Files, affected(f))
	}
	must(t, db.Begin(ctx, opA, p, t0))
	b := rows["notes/b.md"]
	st := FileStat{Size: 2, Device: 2049, Inode: b.Stat.Inode, MtimeNS: 3, CtimeNS: 99}
	must(t, db.CompleteMove(ctx, opA, map[string]FileStat{b.ID: st}, t0))
	for _, f := range under {
		got, err := db.FileByID(ctx, f.ID)
		must(t, err)
		if got.Path != "archive/"+f.Path || got.Generation != f.Generation+1 {
			t.Fatalf("moved row %+v, was %+v", got, f)
		}
		if f.ID == b.ID && got.Stat != st || f.ID != b.ID && got.Stat != f.Stat {
			t.Fatalf("moved row's stat %+v", got.Stat)
		}
	}
	if x, err := db.FileByPath(ctx, "notes_x.md"); err != nil || x.Generation != 1 {
		t.Fatalf("the sibling: %+v, %v", x, err)
	}
	if h, err := db.Holds(ctx); err != nil || len(h) != 0 {
		t.Fatalf("holds = %+v, %v; want the one under the source released", h, err)
	}
	noIntent(t, db)
}

// TestFlag: a flagged intent stays, is listed as flagged, and can no longer be completed or
// dropped as an open one.
func TestFlag(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	must(t, db.Begin(ctx, opA, aSave(rows["a.md"]), t0))
	must(t, db.Flag(ctx, opA))
	if in := onlyIntent(t, db); !in.Flagged {
		t.Fatalf("intent %+v, want flagged", in)
	}
	if err := db.CompleteSave(ctx, opA, FileStat{}); !errors.Is(err, ErrNoIntent) {
		t.Fatalf("completing a flagged intent = %v, want ErrNoIntent", err)
	}
	if err := db.Drop(ctx, opA); !errors.Is(err, ErrNoIntent) {
		t.Fatalf("dropping a flagged intent = %v, want ErrNoIntent", err)
	}
	if err := db.Flag(ctx, opA); !errors.Is(err, ErrNoIntent) {
		t.Fatalf("flagging it again = %v, want ErrNoIntent", err)
	}
}
