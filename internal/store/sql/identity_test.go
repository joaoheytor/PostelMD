//go:build linux

package sql

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

const testVaultUUID = "6f1c2a7e-3b4d-4c5e-8f90-a1b2c3d4e5f6"

func sum(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

// anAssignment is a vault of three files (one a busy writer kept from being hashed, one with
// a name that isn't UTF-8) and a trash of two items, one per layout (ADR-0022).
func anAssignment(now time.Time) Assignment {
	return Assignment{
		VaultUUID: testVaultUUID,
		Files: []NewFile{
			{Path: "a.md", SHA256: sum(1), Stat: FileStat{Size: 1, Device: 2049, Inode: 11, MtimeNS: 1, CtimeNS: 2}},
			{Path: "notes/b.md", SHA256: sum(2), Stat: FileStat{Size: 2, Device: 2049, Inode: 1<<63 + 5, MtimeNS: 3, CtimeNS: 4}},
			{Path: "notes/\xff\xfe.md", Stat: FileStat{Size: 3, Device: 2049, Inode: 13, MtimeNS: 5, CtimeNS: 6}},
		},
		Trash: []TrashImport{
			{OpID: "20240102T030405.000000000Z", Name: "old.md", Kind: "file", DeletedAt: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)},
			{OpID: "20240103T030405.000000000Z-drafts", Name: "drafts", Kind: "folder", DeletedAt: time.Date(2024, 1, 3, 3, 4, 5, 0, time.UTC)},
		},
		Now: now,
	}
}

func TestAssignInOneTransaction(t *testing.T) {
	db, _ := newDB(t)
	ctx := context.Background()
	if id, err := db.Identity(ctx); err != nil || id.Assigned || id.VaultUUID != "" {
		t.Fatalf("a new database: %+v, %v; want nothing assigned", id, err)
	}
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	a := anAssignment(now)
	if err := db.Assign(ctx, a); err != nil {
		t.Fatal(err)
	}
	id, err := db.Identity(ctx)
	if err != nil || !id.Assigned || id.VaultUUID != testVaultUUID || id.RebuildUUID != "" {
		t.Fatalf("Identity = %+v, %v", id, err)
	}
	ids := map[string]bool{}
	for _, nf := range a.Files {
		f, err := db.FileByPath(ctx, nf.Path)
		if err != nil {
			t.Fatalf("FileByPath(%q): %v", nf.Path, err)
		}
		if !ValidID(f.ID) || ids[f.ID] || !idTime(f.ID).Equal(now) {
			t.Fatalf("%q got ID %q: not a new ULID at the assignment's time", nf.Path, f.ID)
		}
		ids[f.ID] = true
		if f.Path != nf.Path || f.Generation != 1 || !bytes.Equal(f.SHA256, nf.SHA256) || f.Stat != nf.Stat || f.Suspect {
			t.Fatalf("FileByPath(%q) = %+v, want %+v at generation 1, live", nf.Path, f, nf)
		}
		if byID, err := db.FileByID(ctx, f.ID); err != nil || byID.Path != nf.Path {
			t.Fatalf("FileByID(%q) = %+v, %v", f.ID, byID, err)
		}
	}
	recs, err := db.TrashRecords(ctx)
	if err != nil || len(recs) != 2 {
		t.Fatalf("TrashRecords = %+v, %v", recs, err)
	}
	for i, r := range recs {
		in := a.Trash[i]
		// Imported: the original path is unknown, and the 7-day clock starts at the import.
		if r.OpID != in.OpID || r.Name != in.Name || r.Kind != in.Kind || r.State != "trashed" || r.OriginalPath != nil ||
			!r.DeletedAt.Equal(in.DeletedAt) || !r.Clock.Equal(now) {
			t.Fatalf("trash record %d = %+v, from %+v", i, r, in)
		}
	}
}

func TestAssignRefusals(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	for _, c := range []struct {
		name string
		edit func(a *Assignment)
		want error
	}{
		{"a vault UUID that isn't one", func(a *Assignment) { a.VaultUUID = "x" }, ErrBadUUID},
		{"an upper-case vault UUID", func(a *Assignment) { a.VaultUUID = "6F1C2A7E-3B4D-4C5E-8F90-A1B2C3D4E5F6" }, ErrBadUUID},
		{"a rebuild UUID that isn't one", func(a *Assignment) { a.RebuildUUID = "../x" }, ErrBadUUID},
		{"two rows for one path", func(a *Assignment) { a.Files = append(a.Files, a.Files[0]) }, nil},
		{"a hash of the wrong length", func(a *Assignment) { a.Files[0].SHA256 = []byte{1} }, nil},
		{"an empty path", func(a *Assignment) { a.Files[0].Path = "" }, nil},
		{"an unknown kind of trash item", func(a *Assignment) { a.Trash[0].Kind = "link" }, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, _ := newDB(t)
			a := anAssignment(now)
			c.edit(&a)
			err := db.Assign(ctx, a)
			if err == nil || c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("Assign = %v, want a refusal (%v)", err, c.want)
			}
			assertNothingAssigned(t, db)
		})
	}
	t.Run("a second assignment", func(t *testing.T) {
		db, _ := newDB(t)
		if err := db.Assign(ctx, anAssignment(now)); err != nil {
			t.Fatal(err)
		}
		before, _ := db.FileByPath(ctx, "a.md")
		again := anAssignment(now)
		again.VaultUUID = "00000000-0000-4000-8000-000000000000"
		if err := db.Assign(ctx, again); !errors.Is(err, ErrAssigned) {
			t.Fatalf("a second Assign = %v, want ErrAssigned", err)
		}
		if id, _ := db.Identity(ctx); id.VaultUUID != testVaultUUID {
			t.Fatalf("a refused Assign changed the vault UUID to %q", id.VaultUUID)
		}
		if after, _ := db.FileByPath(ctx, "a.md"); after.ID != before.ID {
			t.Fatal("a refused Assign changed an ID")
		}
	})
}

// assertNothingAssigned: no flag, no vault UUID, no row in any identity table.
func assertNothingAssigned(t *testing.T, db *DB) {
	t.Helper()
	if id, err := db.Identity(context.Background()); err != nil || id != (Identity{}) {
		t.Fatalf("Identity = %+v, %v; want nothing", id, err)
	}
	for _, table := range []string{"files", "pending", "tombstones", "trash_records"} {
		var n int
		if err := db.r.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s has %d rows (%v)", table, n, err)
		}
	}
}

func TestAssignRecordsTheRebuildUUID(t *testing.T) {
	db, _ := newDB(t)
	a := anAssignment(time.Now())
	a.RebuildUUID = "11111111-2222-4333-8444-555555555555"
	if err := db.Assign(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if id, err := db.Identity(context.Background()); err != nil || id.RebuildUUID != a.RebuildUUID {
		t.Fatalf("Identity = %+v, %v", id, err)
	}
}

func TestLookupsOfAbsentRows(t *testing.T) {
	db, _ := newDB(t)
	ctx := context.Background()
	if _, err := db.FileByPath(ctx, "nope.md"); !errors.Is(err, ErrNoFile) {
		t.Fatalf("FileByPath = %v, want ErrNoFile", err)
	}
	if _, err := db.FileByID(ctx, NewID(time.Now())); !errors.Is(err, ErrNoFile) {
		t.Fatalf("FileByID = %v, want ErrNoFile", err)
	}
	if _, err := db.FileByID(ctx, "../x"); !errors.Is(err, ErrNoFile) {
		t.Fatalf("FileByID of a malformed ID = %v, want ErrNoFile", err)
	}
	if recs, err := db.TrashRecords(ctx); err != nil || len(recs) != 0 {
		t.Fatalf("TrashRecords = %v, %v", recs, err)
	}
}

// The schema refuses rows the code must never write (ADR-0031): a suspect without the time
// it was first missed, a live row with one, an unknown state or reason, a generation below 1.
func TestSchemaConstraints(t *testing.T) {
	db, _ := newDB(t)
	id := NewID(time.Now())
	row := "INSERT INTO files (id, path, generation, size, device, inode, mtime_ns, ctime_ns, state, suspect_since_ms) VALUES "
	for _, c := range []struct {
		stmt string
		ok   bool
	}{
		{row + "('" + id + "', x'61', 1, 0, 1, 1, 0, 0, 'live', NULL)", true},
		{row + "('" + NewID(time.Now()) + "', x'62', 1, 0, 1, 1, 0, 0, 'suspect', NULL)", false},
		{row + "('" + NewID(time.Now()) + "', x'63', 1, 0, 1, 1, 0, 0, 'live', 5)", false},
		{row + "('" + NewID(time.Now()) + "', x'64', 1, 0, 1, 1, 0, 0, 'gone', NULL)", false},
		{row + "('" + NewID(time.Now()) + "', x'65', 0, 0, 1, 1, 0, 0, 'live', NULL)", false},
		{row + "('" + NewID(time.Now()) + "', x'61', 1, 0, 1, 1, 0, 0, 'live', NULL)", false}, // a path twice
		{row + "('short', x'66', 1, 0, 1, 1, 0, 0, 'live', NULL)", false},
		{"INSERT INTO tombstones VALUES ('" + NewID(time.Now()) + "', x'61', 1, 0, 'forgotten')", false},
		{"INSERT INTO tombstones VALUES ('" + NewID(time.Now()) + "', x'61', 1, 0, 'ambiguous-rename')", true},
		{"INSERT INTO trash_records VALUES ('op', x'61', NULL, 'file', 'lost', 0, 0)", false},
		{"INSERT INTO pending VALUES (x'70', '" + NewID(time.Now()) + "', zeroblob(32), 0, 1, 1, 0, 0)", false}, // no such file
		{"INSERT INTO pending VALUES (x'70', '" + id + "', zeroblob(32), 0, 1, 1, 0, 0)", true},
	} {
		_, err := db.w.Exec(c.stmt)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v (want ok: %v)", c.stmt, err, c.ok)
		}
	}
	// Removing a suspect's row drops the path held for it (ADR-0031, rule 2).
	if _, err := db.w.Exec("DELETE FROM files WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.w.QueryRow("SELECT count(*) FROM pending").Scan(&n); err != nil || n != 0 {
		t.Fatalf("pending kept %d rows after its file's row went (%v)", n, err)
	}
}

// version1 is the migrations of the builds before this slice (#172–#175).
func version1(t *testing.T) fstest.MapFS {
	t.Helper()
	v1 := fstest.MapFS{}
	for name, f := range embeddedFS(t) {
		if name[:4] == "0001" {
			v1[name] = f
		}
	}
	return v1
}

// The runbook's rollback to a version-1 build at format 1 (running-v0.1.md): with the
// server stopped, app.db, its -wal and -shm, the snapshot and the post_migration_started
// file move into a new folder; the older build then starts with a new database, and the
// next upgrade migrates again. Leaving the snapshot behind makes that upgrade refuse.
func TestRunbookRollbackToAVersion1Build(t *testing.T) {
	for _, moveSnapshot := range []bool{true, false} {
		dir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		openWith(t, dir, version1(t))
		openWith(t, dir, embeddedFS(t)) // the upgrade: 1 → 2
		aside := filepath.Join(filepath.Dir(dir), "rolled-back")
		if err := os.Mkdir(aside, 0o700); err != nil {
			t.Fatal(err)
		}
		move := []string{AppDB, AppDB + "-wal", AppDB + "-shm", markerName(1, 2)}
		if moveSnapshot {
			move = append(move, snapshotName(1, 2))
		}
		for _, name := range move {
			if err := os.Rename(filepath.Join(dir, name), filepath.Join(aside, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
		openWith(t, dir, version1(t)) // the older build starts
		db, err := Open(context.Background(), dir, discard)
		if !moveSnapshot {
			if !errors.Is(err, ErrMigrateInterference) {
				t.Fatalf("upgrading again beside the old snapshot = %v, want ErrMigrateInterference", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("upgrading again: %v", err)
		}
		assertMigrated(t, db, dir, 1, len(embeddedFS(t)))
		_ = db.Close()
	}
}

// A database an earlier build's serve created (version 1, no flag) migrates to this build's
// version through the pre-migration snapshot, and has nothing assigned (ADR-0034: the format
// table's "without the flag" rows).
func TestMigrateAVersion1Database(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	openWith(t, dir, version1(t))
	db, err := Open(context.Background(), dir, discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	last := len(embeddedFS(t))
	assertMigrated(t, db, dir, 1, last)
	assertNothingAssigned(t, db)
}
