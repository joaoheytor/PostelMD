package fs

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

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

// TestIsReservedGitLookalikes: the names git refuses in a tree because Windows (NTFS) or
// macOS (HFS+) would treat them as .git (git's verify_path, CVE-2014-9390) are reserved too,
// so a vault never holds a path that can't be pushed or cloned safely (#119).
func TestIsReservedGitLookalikes(t *testing.T) {
	var hfs []string // .git with each code point HFS+ ignores, at each place
	for _, r := range gitHFSIgnorable {
		u := string(r)
		hfs = append(hfs, u+".git", "."+u+"git", ".g"+u+"it", ".gi"+u+"t", ".git"+u, ".G"+u+u+"IT")
	}
	zwnj, zwsp, bom := string(rune(0x200c)), string(rune(0x200b)), string(rune(0xfeff))
	for _, tc := range []struct {
		name     string
		reserved []string
		allowed  []string
	}{
		{"NTFS trailing spaces and dots",
			[]string{".git ", ".git.", ".git. . ", ".GIT  ", "notes/.git /config", "a/.Git.../b.md"},
			[]string{".git.md", ".gitx", " .git", "..git", ".git-notes.md", "x.git"}},
		{"NTFS short name",
			[]string{"git~1", "GIT~1", "Git~1", "git~1 ", "git~1.", "a/git~1/x.md"},
			[]string{"git~2", "git~10", "git~1x", "git~1.md", ".git~1", "agit~1", "git", "git~"}},
		{"NTFS alternate data stream",
			[]string{".git::$INDEX_ALLOCATION", ".git:x", ".git .:y", "git~1:z"},
			[]string{"a:.git", ".gitx:y"}},
		{"backslash, a separator on Windows (any backslash is refused)",
			[]string{`a\.git`, `.git\config`, `x\git~1 .`, `a\b`},
			nil},
		{"HFS+ ignorable code points",
			append(hfs, "notes/.g"+zwnj+"it/config"),
			// U+200B (zero-width space) isn't among the code points HFS+ ignores, nor in git's
			// list; HFS+ folds neither a dotless i nor an i with diaeresis to i.
			[]string{zwsp + ".git", ".g" + zwsp + "it", ".g" + string(rune(0x131)) + "t", ".g" + string(rune(0xef)) + "t"}},
		{"both at once",
			[]string{".g" + zwnj + "it ", bom + "git~1."},
			nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range tc.reserved {
				if !IsReserved(p) {
					t.Errorf("IsReserved(%q) = false, want true", p)
				}
			}
			for _, p := range tc.allowed {
				if IsReserved(p) {
					t.Errorf("IsReserved(%q) = true, want false", p)
				}
			}
		})
	}
}

func FuzzIsReserved(f *testing.F) {
	for _, s := range []string{".git/config", "notes/a.md", ".git ", "a/.git./b", "git~1",
		".g" + string(rune(0x200c)) + "it", ".git::$INDEX_ALLOCATION", `a\.git`,
		string(rune(0xfeff)) + ".GIT", ".gitx"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		// Anything checkRel refuses is reserved, so a path that passes IsReserved is always a
		// valid relative path for the rest of the package.
		if !IsReserved(p) && checkRel(p) != nil {
			t.Fatalf("IsReserved(%q) = false but checkRel refuses it", p)
		}
		// Every component git's own checks call .git is reserved.
		for _, c := range strings.Split(p, "/") {
			if (gitIsNTFSDotGit(c) || gitIsHFSDotGit(c)) && !IsReserved(p) {
				t.Fatalf("IsReserved(%q) = false, but git treats %q as .git", p, c)
			}
		}
	})
}

// gitIsNTFSDotGit is a line-by-line port of git's is_ntfs_dotgit (path.c), the reference
// the fuzz target checks IsReserved against. A C string ends at NUL, so a byte past the end
// reads as 0.
func gitIsNTFSDotGit(name string) bool {
	at := func(i int) byte {
		if i < len(name) {
			return name[i]
		}
		return 0
	}
	var i int
	switch c := at(0); {
	case c == '.':
		if lower(at(1)) != 'g' || lower(at(2)) != 'i' || lower(at(3)) != 't' {
			return false
		}
		i = 4
	case lower(c) == 'g':
		if lower(at(1)) != 'i' || lower(at(2)) != 't' || at(3) != '~' || at(4) != '1' {
			return false
		}
		i = 5
	default:
		return false
	}
	for ; ; i++ {
		c := at(i)
		if c == 0 || c == '/' || c == '\\' || c == ':' {
			return true
		}
		if c != '.' && c != ' ' {
			return false
		}
	}
}

// gitIsHFSDotGit is a port of git's is_hfs_dotgit (utf8.c): ".git" in any ASCII case once
// the code points HFS+ ignores are dropped; malformed UTF-8 is never .git.
func gitIsHFSDotGit(name string) bool {
	next := func() (rune, bool) {
		for {
			if name == "" {
				return 0, true
			}
			r, n := utf8.DecodeRuneInString(name)
			if r == utf8.RuneError && n <= 1 {
				return 0, false
			}
			name = name[n:]
			if !slices.Contains(gitHFSIgnorable, r) {
				return r, true
			}
		}
	}
	for _, want := range ".git" {
		r, ok := next()
		if !ok || r > 127 || lower(r) != want {
			return false
		}
	}
	r, ok := next()
	return ok && (r == 0 || r == '/')
}

// gitHFSIgnorable are the code points git's next_hfs_char (utf8.c) skips, as HFS+ does:
// zero-width (non-)joiners, direction marks and overrides, the deprecated format characters
// U+206A–U+206F, and the zero-width no-break space. They are spelled out as numbers because
// written literally they would be invisible here.
var gitHFSIgnorable = []rune{0x200c, 0x200d, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d,
	0x202e, 0x206a, 0x206b, 0x206c, 0x206d, 0x206e, 0x206f, 0xfeff}

// lower is C's tolower in the C locale.
func lower[T byte | rune](c T) T {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
