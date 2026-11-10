//go:build linux

package http

import (
	iofs "io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

// reservedCases are the names store.IsReserved refuses (internal/store/fs/reserved.go), each
// as a folder and a file inside it, so every route can be tried on both.
var reservedCases = []struct{ name, dir, file string }{
	{"git", ".git", ".git/config"},
	{"git, other case", ".GIT", ".GIT/config"},
	{"git, nested", "notes/.git", "notes/.git/config"},
	{"app state", ".postelmd", ".postelmd/state.json"},
	{"trash", vault.TrashDir, vault.TrashDir + "/old.md"},
	{"temp", vault.TmpDir, vault.TmpDir + "/inflight"},
	{"syncthing folder marker", ".stfolder", ".stfolder/marker"},
	{"syncthing versions", ".stversions", ".stversions/a.md"},
	{"sync conflict copy", "notes/b.sync-conflict-20260101-000000-ABCDEFG", "notes/a.sync-conflict-20260101-000000-ABCDEFG.md"},
}

// TestReservedPathsOverHTTP tries every route that takes a path on every reserved name: each
// answers 404 (ADR-0006: not 403, which would confirm the path exists) and nothing on disk
// changes.
func TestReservedPathsOverHTTP(t *testing.T) {
	const secret = "do not touch"
	jsonCT := map[string]string{"Content-Type": "application/json"}
	for _, rc := range reservedCases {
		t.Run(rc.name, func(t *testing.T) {
			srv, dir := newAPI(t)
			for _, d := range []string{rc.dir, filepath.Dir(rc.file)} {
				if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, rc.file), []byte(secret), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, dir)
			match := map[string]string{"If-Match": etag(vault.RevOf([]byte(secret)))}
			for _, c := range []call{
				{method: "GET", path: "/api/v1/notes/" + rc.file},
				{method: "PUT", path: "/api/v1/notes/" + rc.dir + "/new.md", body: "N", header: map[string]string{"If-None-Match": "*"}},
				{method: "PUT", path: "/api/v1/notes/" + rc.file, body: "N", header: match},
				{method: "DELETE", path: "/api/v1/notes/" + rc.file},
				{method: "GET", path: "/api/v1/folders/" + rc.dir},
				{method: "POST", path: "/api/v1/folders/" + rc.dir + "/sub"},
				{method: "DELETE", path: "/api/v1/folders/" + rc.dir},
				{method: "POST", path: "/api/v1/move", header: jsonCT, body: `{"from":"` + rc.file + `","to":"notes/moved.md"}`},
				{method: "POST", path: "/api/v1/move", header: jsonCT, body: `{"from":"` + rc.dir + `","to":"moved"}`},
				{method: "POST", path: "/api/v1/move", header: jsonCT, body: `{"from":"notes/a.md","to":"` + rc.dir + `/a.md"}`},
				{method: "POST", path: "/api/v1/move", header: jsonCT, body: `{"from":"notes/a.md","to":"` + rc.file + `.md"}`},
			} {
				if resp, body := do(t, srv, c); resp.StatusCode != 404 {
					t.Errorf("%s %s %s = %d %q, want 404", c.method, c.path, c.body, resp.StatusCode, body)
				}
			}
			if after := snapshot(t, dir); !maps.Equal(before, after) {
				t.Fatalf("the vault changed:\nbefore %v\nafter  %v", slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
			}
		})
	}
}

// snapshot maps every path under dir to its content (files) or "dir/" (folders).
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			out[rel] = "dir/"
			return nil
		}
		b, err := os.ReadFile(p) //nolint:gosec // G304: a test reading its own temp dir
		out[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
