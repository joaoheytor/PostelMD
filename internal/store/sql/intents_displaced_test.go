//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// A vacated row (ADR-0033, "Giving a path a row") as the store's readers see it, and the
// constraint that keeps its placeholder path and displaced_from together.

// TestADisplacedRowReadsAsDisplaced: FileByID and Files return a vacated row with the path
// it had in DisplacedFrom; its Path is the placeholder, which no lookup by path finds. A
// caller outside the scan treats such a row as missing.
func TestADisplacedRowReadsAsDisplaced(t *testing.T) {
	db, rows := assigned(t)
	ctx := context.Background()
	a := rows["a.md"]
	must(t, db.Begin(ctx, opA, CreateIntent{Path: "a.md", NewSHA256: sum(5)}, t0))
	_, err := db.CompleteCreate(ctx, opA, FileStat{Size: 5, Inode: 50}, t0)
	must(t, err)
	got, err := db.FileByID(ctx, a.ID)
	must(t, err)
	if !got.Displaced() || got.DisplacedFrom != "a.md" || !strings.HasPrefix(got.Path, "\x00") {
		t.Fatalf("FileByID of a vacated row = %+v; want it displaced from a.md, on a placeholder", got)
	}
	if _, err := db.FileByPath(ctx, got.DisplacedFrom); err == nil {
		if f, _ := db.FileByPath(ctx, got.DisplacedFrom); f.ID == a.ID {
			t.Fatal("FileByPath of the path it had still finds the vacated row")
		}
	}
	all, err := db.Files(ctx)
	must(t, err)
	for _, f := range all {
		if (f.ID == a.ID) != f.Displaced() {
			t.Fatalf("Files: %+v; want only a.md's old row displaced", f)
		}
	}
}

// TestTheDisplacedCheck: displaced_from is set exactly while the path is a placeholder, and
// only on a suspect row; each half of the CHECK refuses its own case.
func TestTheDisplacedCheck(t *testing.T) {
	db, _ := assigned(t)
	ctx := context.Background()
	insert := func(path string, from any, suspect bool) error {
		id := NewID(t0)
		if path == "" {
			path = string(displacedPath(id))
		}
		state, since := "live", any(nil)
		if suspect {
			state, since = "suspect", t0.UnixMilli()
		}
		return db.writeTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO files (id, path, generation, sha256, size, device, inode, mtime_ns,
				ctime_ns, state, suspect_since_ms, displaced_from) VALUES (?, ?, 1, NULL, 0, 0, 0, 0, 0, ?, ?, ?)`,
				id, []byte(path), state, since, from)
			return err
		})
	}
	if err := insert("", []byte("q.md"), true); err != nil {
		t.Fatalf("a vacated row: %v", err)
	}
	for name, err := range map[string]error{
		"a real path with displaced_from":   insert("q.md", []byte("q.md"), true),
		"a placeholder without it":          insert("", nil, true),
		"a vacated row that is live":        insert("", []byte("q.md"), false),
		"an empty displaced_from":           insert("", []byte{}, true),
		"a real path, live, displaced_from": insert("r.md", []byte("r.md"), false),
	} {
		if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Errorf("%s: %v; want a CHECK failure", name, err)
		}
	}
}

// TestAMoveOutOfItsDestinationIsRefused: a source under the destination can't be moved there
// (the destination exists: the no-clobber rename fails), so the payload refuses it, and no
// completion has to keep moving rows from being vacated.
func TestAMoveOutOfItsDestinationIsRefused(t *testing.T) {
	db, rows := assigned(t)
	b := rows["notes/b.md"]
	p := MoveIntent{From: "notes/b.md", To: "notes", Device: 2049, Inode: b.Stat.Inode, Files: []Affected{affected(b)}, Mechanism: MechanismNoReplace}
	if err := db.Begin(context.Background(), opA, p, t0); !errors.Is(err, ErrBadIntent) {
		t.Fatalf("Begin of a move out of its destination = %v, want ErrBadIntent", err)
	}
}
