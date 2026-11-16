//go:build linux

package sql

import (
	"context"
	"slices"
	"testing"
	"time"
)

// A read's rows: the same hash, or the same device and inode (the full 64 bits), live or
// suspect, and the rows that hold a path.
func TestFilesMatching(t *testing.T) {
	db, before := assigned(t)
	ctx := context.Background()
	a, b, u := before["a.md"], before["notes/b.md"], before["notes/\xff\xfe.md"]
	if _, err := db.ApplyScan(ctx, time.Now(), [][]Change{{Suspect{Row: rowOf(u)}, Hold{Path: "held.md", FileID: u.ID, SHA256: sum(8)}}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		sum      []byte
		dev, ino uint64
		want     []string
	}{
		{"by hash", a.SHA256, 1, 1, []string{"a.md", "notes/\xff\xfe.md"}},
		{"by inode", sum(9), b.Stat.Device, b.Stat.Inode, []string{"notes/b.md", "notes/\xff\xfe.md"}},
		{"another device", sum(9), b.Stat.Device + 1, b.Stat.Inode, []string{"notes/\xff\xfe.md"}},
	} {
		files, err := db.FilesMatching(ctx, c.sum, c.dev, c.ino)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range files {
			got = append(got, f.Path)
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Fatalf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// Suspect restarts a suspect's clock only when it is in the future (the clock stepped back).
func TestSuspectRestartsAClockInTheFuture(t *testing.T) {
	db, before := assigned(t)
	ctx := context.Background()
	b := before["notes/b.md"]
	t0 := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{t0, t0.Add(-time.Hour)} {
		if applied, err := db.ApplyScan(ctx, at, [][]Change{{Suspect{Row: rowOf(b)}}}); err != nil || !applied[0] {
			t.Fatalf("ApplyScan at %v = %v, %v", at, applied, err)
		}
	}
	if r := rowsByPath(t, db)["notes/b.md"]; !r.SuspectSince.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("suspect since %v, want %v", r.SuspectSince, t0.Add(-time.Hour))
	}
}
