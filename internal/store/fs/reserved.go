package fs

import (
	"strings"
	"unicode"
)

// reservedNames are path components that are reserved at any depth (ADR-0006, "Reserved
// paths"), compared case-insensitively. The trash and temp directories are only created at
// the vault root, but refusing them anywhere is simpler and loses nothing. ".git" and the
// names git treats as ".git" are matched by IsDotGit.
var reservedNames = map[string]bool{
	".postelmd":       true,
	".postelmd-trash": true,
	".postelmd-tmp":   true,
	".stfolder":       true,
	".stversions":     true,
}

// IsReserved reports whether a vault-relative path must never be reached through the API
// (ADR-0006). Callers apply it to every path a user supplies, for every operation, and
// answer 404 for a reserved one. Malformed paths count as reserved, so a path that passes
// is also a valid path for Root.
//
// `.postelmd-secrets-allow` is readable but writable only through the findings API, which
// arrives with the secret gates (ADR-0020); until then it is an ordinary file.
func IsReserved(p string) bool {
	if checkRel(p) != nil {
		return true
	}
	for _, c := range strings.Split(p, "/") {
		lc := strings.ToLower(c)
		if c == "" || c == "." || c == ".." || reservedNames[lc] || IsDotGit(c) || strings.Contains(lc, ".sync-conflict-") {
			return true
		}
		if strings.IndexFunc(c, unicode.IsControl) >= 0 {
			return true
		}
	}
	return false
}

// IsDotGit reports whether a path component is ".git" in any letter case, or a name that
// git refuses in a tree because a filesystem a clone may land on treats it as ".git"
// (git's verify_path, CVE-2014-9390):
//   - NTFS (is_ntfs_dotgit): trailing spaces and dots are dropped, a colon starts an
//     alternate data stream, and "git~1" is the short name of ".git" (a backslash, a folder
//     separator on Windows, is already refused by checkRel);
//   - HFS+ (is_hfs_dotgit) ignores some code points, and Linux casefold on older kernels
//     ignored every default-ignorable one: both are dropped (see ignorable).
//
// It applies all of them at once, which refuses a little more than git does
// (".g<U+200C>it "): no such name is a usable note. Store/git refuses the same names in a
// tree it writes.
func IsDotGit(c string) bool {
	c, _, _ = strings.Cut(dropIgnorable(c), ":")
	c = strings.TrimRight(c, " .")
	return foldEqual(c, ".git") || foldEqual(c, "git~1")
}

// FoldsToDotGit reports whether a case-folding Linux filesystem may find the name c when git
// looks up ".git": c is ".git" in any letter case once ignorable code points are dropped.
func FoldsToDotGit(c string) bool {
	return foldEqual(dropIgnorable(c), ".git")
}

// foldEqual compares s with the lower-case ASCII name in any letter case. strings.ToLower
// maps U+0130 (I with a dot above) to i, which EqualFold doesn't: check both.
func foldEqual(s, name string) bool {
	return strings.ToLower(s) == name || strings.EqualFold(s, name)
}

func dropIgnorable(c string) string {
	return strings.Map(func(r rune) rune {
		if ignorable(r) {
			return -1
		}
		return r
	}, c)
}

// ignorable reports whether a filesystem may ignore r in a name: format characters (which
// include the 16 code points git's next_hfs_char skips for HFS+, utf8.c), the other
// default-ignorable code points and the variation selectors, a superset of what Linux
// casefold ignored on older kernels.
func ignorable(r rune) bool {
	return unicode.In(r, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector)
}
