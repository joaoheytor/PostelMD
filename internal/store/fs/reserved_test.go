package fs

import "testing"

func TestIsReserved(t *testing.T) {
	reserved := []string{
		".git", ".git/config", "notes/.git", "notes/.git/HEAD", ".GIT/config", ".Git",
		".postelmd", ".postelmd/auth.json", "a/.postelmd/x", ".PostelMD",
		".postelmd-trash", ".postelmd-trash/x.md", ".postelmd-tmp", ".postelmd-tmp/op1",
		".POSTELMD-TMP/op1",
		".stfolder", "a/.stversions/x.md", "note.sync-conflict-20260101-120000-ABCDEF.md",
		"a/b.SYNC-CONFLICT-x.md",
		"", ".", "..", "a/../b", "a//b", "/abs", "a/./b", "a/b/", "a\x00b", "a\nb", "a\x7fb",
	}
	allowed := []string{
		"note.md", "runbooks/db-failover.md", ".github/workflows/ci.yaml", ".gitignore",
		"git/notes.md", "postelmd.md", ".postelmd-notes.md", "a/.gitkeep", "sync-conflict.md",
		"_assets/diagram.png", "über/ä.md",
	}
	for _, p := range reserved {
		if !IsReserved(p) {
			t.Errorf("IsReserved(%q) = false, want true", p)
		}
	}
	for _, p := range allowed {
		if IsReserved(p) {
			t.Errorf("IsReserved(%q) = true, want false", p)
		}
	}
}

func FuzzIsReserved(f *testing.F) {
	f.Add(".git/config")
	f.Add("notes/a.md")
	f.Fuzz(func(t *testing.T, p string) {
		// Anything checkRel refuses is reserved, so a path that passes IsReserved is always a
		// valid relative path for the rest of the package.
		if !IsReserved(p) && checkRel(p) != nil {
			t.Fatalf("IsReserved(%q) = false but checkRel refuses it", p)
		}
	})
}
