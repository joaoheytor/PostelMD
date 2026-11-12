//go:build linux

package vault

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	stamp1 = "20261109T100000.000000000Z" // the oldest
	stamp2 = "20261110T143005.000000000Z"
	stamp3 = "20261110T143005.000000000Z-1" // a second delete in the same instant
	stamp4 = "20261111T090000.500000000Z"
)

// trashFixture fills the trash with both layouts (ADR-0022) and things a listing must skip.
func trashFixture(t *testing.T, dir string) {
	t.Helper()
	writeFiles(t, dir, map[string]string{
		TrashDir + "/" + stamp1 + "-old.md":                              "old",  // old layout, a file
		TrashDir + "/" + stamp1 + "-1-x/inside.md":                       "in",   // old layout, a folder
		TrashDir + "/" + stamp2 + "/a.md":                                "AAAA", // new layout, a file
		TrashDir + "/" + stamp3 + "/dir/n.md":                            "N",    // new layout, a folder
		TrashDir + "/" + stamp4 + "/" + strings.Repeat("l", 252) + ".md": "L",
		TrashDir + "/not-a-stamp.md":                                     "junk", // not made by a delete
	})
	must(t, os.Mkdir(filepath.Join(dir, TrashDir, "20261112T000000.000000000Z"), 0o700)) // a crash's empty leftover
	must(t, os.Symlink("/etc/passwd", filepath.Join(dir, TrashDir, "20261113T000000.000000000Z-link")))
}

// TestListTrash: both layouts, newest first, with the original name, kind, time and size;
// empty stamp folders, symlinks and entries no delete makes are skipped.
func TestListTrash(t *testing.T) {
	v, dir := newVault(t)
	trashFixture(t, dir)
	items, truncated, err := v.ListTrash(100)
	must(t, err)
	type row struct {
		id, name string
		kind     Kind
		size     int64
		at       string
	}
	var got []row
	for _, it := range items {
		var size int64 = -1
		if it.Size != nil {
			size = *it.Size
		}
		got = append(got, row{it.ID, it.Name, it.Kind, size, it.DeletedAt.Format(time.RFC3339Nano)})
	}
	want := []row{
		{stamp4, strings.Repeat("l", 252) + ".md", KindFile, 1, "2026-11-11T09:00:00.5Z"},
		{stamp3, "dir", KindFolder, -1, "2026-11-10T14:30:05Z"},
		{stamp2, "a.md", KindFile, 4, "2026-11-10T14:30:05Z"},
		{stamp1 + "-old.md", "old.md", KindFile, 3, "2026-11-09T10:00:00Z"},
		{stamp1 + "-1-x", "1-x", KindFolder, -1, "2026-11-09T10:00:00Z"},
	}
	if truncated || len(got) != len(want) {
		t.Fatalf("ListTrash = %+v (truncated %v), want %+v", got, truncated, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	items, truncated, err = v.ListTrash(2)
	must(t, err)
	if !truncated || len(items) != 2 || items[0].ID != stamp4 || items[1].ID != stamp3 {
		t.Fatalf("ListTrash(2) = %+v, truncated %v; want the two newest, truncated", items, truncated)
	}
}

// TestListTrashEmpty: no trash folder yet, or an empty one, lists nothing.
func TestListTrashEmpty(t *testing.T) {
	v, _ := newVault(t)
	items, truncated, err := v.ListTrash(10)
	if err != nil || len(items) != 0 || truncated {
		t.Fatalf("ListTrash = %v, %v, %v", items, truncated, err)
	}
}

// TestSymlinkedTrashFailsClosed: a trash folder replaced by a symlink (to a folder of
// stamp-named entries) is never followed: the listing is empty, and nothing restores from it.
func TestSymlinkedTrashFailsClosed(t *testing.T) {
	v, dir := newVault(t)
	elsewhere := t.TempDir()
	writeFiles(t, elsewhere, map[string]string{stamp2 + "/a.md": "outside"})
	must(t, os.RemoveAll(filepath.Join(dir, TrashDir)))
	must(t, os.Symlink(elsewhere, filepath.Join(dir, TrashDir)))
	items, truncated, err := v.ListTrash(10)
	if err != nil || len(items) != 0 || truncated {
		t.Fatalf("ListTrash = %v, %v, %v; want an empty list", items, truncated, err)
	}
	if _, err := v.RestoreTrashed(ctx, stamp2, "", "b.md"); !errors.Is(err, ErrNotInTrash) {
		t.Fatalf("RestoreTrashed = %v, want ErrNotInTrash", err)
	}
	if c := content(t, dir, "b.md"); c != "<missing>" {
		t.Fatalf("b.md holds %q", c)
	}
}

// TestRestoreFromTrash: an item goes back, with its content, to the folder and name asked
// for; the emptied stamp folder is removed, and the item is no longer listed.
func TestRestoreFromTrash(t *testing.T) {
	for _, tc := range []struct {
		name, id, folder, as string
		want, file, content  string // file: a file to check below the restored path
	}{
		{"new layout to the root", stamp2, "", "", "a.md", "", "AAAA"},
		{"new layout to a folder, renamed", stamp2, "notes", "b.md", "notes/b.md", "", "AAAA"},
		{"new layout folder", stamp3, "notes", "", "notes/dir", "/n.md", "N"},
		{"old layout file", stamp1 + "-old.md", "", "", "old.md", "", "old"},
		{"old layout folder", stamp1 + "-1-x", "", "y", "y", "/inside.md", "in"},
		{"255-byte name", stamp4, "", "", strings.Repeat("l", 252) + ".md", "", "L"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, dir := newVault(t)
			trashFixture(t, dir)
			got, err := v.RestoreTrashed(ctx, tc.id, tc.folder, tc.as)
			must(t, err)
			if got != tc.want {
				t.Fatalf("restored to %q, want %q", got, tc.want)
			}
			if c := content(t, dir, tc.want+tc.file); c != tc.content {
				t.Fatalf("%s holds %q", tc.want+tc.file, c)
			}
			if _, err := os.Lstat(filepath.Join(dir, TrashDir, tc.id)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("trash entry %s still there (%v)", tc.id, err)
			}
			items, _, err := v.ListTrash(100)
			must(t, err)
			for _, it := range items {
				if it.ID == tc.id {
					t.Fatalf("%s is still listed", tc.id)
				}
			}
		})
	}
}

// TestRestoreNeverOverwrites: an existing destination is ErrExists, and both it and the
// trashed item stay as they were.
func TestRestoreNeverOverwrites(t *testing.T) {
	v, dir := newVault(t)
	trashFixture(t, dir)
	writeFiles(t, dir, map[string]string{"notes/a.md": "current"})
	if _, err := v.RestoreTrashed(ctx, stamp2, "notes", ""); !errors.Is(err, ErrExists) {
		t.Fatalf("RestoreTrashed = %v, want ErrExists", err)
	}
	if c := content(t, dir, "notes/a.md"); c != "current" {
		t.Fatalf("the destination holds %q", c)
	}
	if c := content(t, dir, TrashDir+"/"+stamp2+"/a.md"); c != "AAAA" {
		t.Fatalf("the trashed item holds %q", c)
	}
	if got, err := v.RestoreTrashed(ctx, stamp2, "notes", "a-2.md"); err != nil || got != "notes/a-2.md" {
		t.Fatalf("restore under another name = %q, %v", got, err)
	}
}

// TestRestoreRefusesBadIDs: the id names a direct child of the trash in one of the two
// layouts, nothing else: never a path into the trash, out of it, or a crash's empty folder.
func TestRestoreRefusesBadIDs(t *testing.T) {
	v, dir := newVault(t)
	trashFixture(t, dir)
	for _, id := range []string{
		"", ".", "..", "../notes/a.md", "a.md", "notes", TrashDir, stamp2 + "/a.md", stamp2 + "/../" + stamp3,
		"20261112T000000.000000000Z",      // the empty leftover
		"20261113T000000.000000000Z-link", // a symlink
		"not-a-stamp.md",                  // in the trash, but no delete made it
		"20261114T000000.000000000Z",      // missing
		stamp2 + "\n",
	} {
		_, err := v.RestoreTrashed(ctx, id, "", "")
		if !errors.Is(err, ErrNotInTrash) || !errors.Is(err, ErrNotFound) {
			t.Errorf("RestoreTrashed(%q) = %v, want ErrNotInTrash", id, err)
		}
	}
	if c := content(t, dir, "notes/a.md"); c != "A" {
		t.Fatalf("notes/a.md holds %q", c)
	}
}

// TestRestoreChecksTheDestination: the destination goes through the same checks as a move's.
func TestRestoreChecksTheDestination(t *testing.T) {
	v, dir := newVault(t)
	trashFixture(t, dir)
	for _, tc := range []struct {
		folder, name string
		want         error
	}{
		{".git", "", ErrNotFound},
		{TrashDir, "", ErrNotFound},
		{TrashDir + "/" + stamp3, "", ErrNotFound},
		{"..", "", ErrNotFound},
		{"notes/../..", "", ErrNotFound},
		{"/notes", "", ErrNotFound},
		{"", ".git", ErrNotFound},
		{"", "..", ErrNotFound},
		{"", "a/b.md", ErrNameHasSlash},
		{"", strings.Repeat("x", 256), ErrNameTooLong},
		{"missing", "", ErrNotFound},
	} {
		_, err := v.RestoreTrashed(ctx, stamp2, tc.folder, tc.name)
		if !errors.Is(err, tc.want) || errors.Is(err, ErrNotInTrash) {
			t.Errorf("RestoreTrashed to %q/%q = %v, want %v", tc.folder, tc.name, err, tc.want)
		}
	}
	if c := content(t, dir, TrashDir+"/"+stamp2+"/a.md"); c != "AAAA" {
		t.Fatalf("the trashed item holds %q", c)
	}
}

// TestRestoreLogsAStuckStampFolderWithoutItsText: a stamp folder that can't be removed
// after a restore is only a warning, logged with the pathless attributes, never the
// error's text (CLAUDE.md rule 15, #144).
func TestRestoreLogsAStuckStampFolderWithoutItsText(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes entries from read-only directories")
	}
	v, dir := newVault(t)
	trashFixture(t, dir)
	var logs bytes.Buffer
	v.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	trash := filepath.Join(dir, TrashDir)
	must(t, os.Chmod(trash, 0o500))                  //nolint:gosec // read-only, but still listable
	t.Cleanup(func() { _ = os.Chmod(trash, 0o700) }) //nolint:gosec // removing a directory needs its x bit
	if _, err := v.RestoreTrashed(ctx, stamp2, "", ""); err != nil {
		t.Fatalf("RestoreTrashed = %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "err.errno=EACCES") || strings.Contains(out, stamp2) {
		t.Fatalf("want a pathless warning, got %q", out)
	}
}
