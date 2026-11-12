package fs

import (
	"strings"
	"unicode"
)

// reservedNames are path components that are reserved at any depth (ADR-0006, "Reserved
// paths"), compared case-insensitively. The trash and temp directories are only created at
// the vault root, but refusing them anywhere is simpler and loses nothing. ".git" and the
// names git treats as ".git" are matched by isDotGit.
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
		if c == "" || c == "." || c == ".." || reservedNames[lc] || isDotGit(c) || strings.Contains(lc, ".sync-conflict-") {
			return true
		}
		if strings.IndexFunc(c, unicode.IsControl) >= 0 {
			return true
		}
	}
	return false
}

// isDotGit reports whether a path component is ".git" in any letter case, or a name that
// git refuses in a tree because a filesystem a clone may land on treats it as ".git"
// (git's verify_path, CVE-2014-9390):
//   - NTFS (is_ntfs_dotgit): trailing spaces and dots are dropped, a colon starts an
//     alternate data stream, and "git~1" is the short name of ".git" (a backslash, a folder
//     separator on Windows, is already refused by checkRel);
//   - HFS+ (is_hfs_dotgit): the code points in hfsIgnorable are ignored.
//
// It applies both at once, which refuses a little more than git does (".g<U+200C>it "): no
// such name is a usable note.
func isDotGit(c string) bool {
	c = strings.Map(func(r rune) rune {
		if hfsIgnorable[r] {
			return -1
		}
		return r
	}, c)
	c, _, _ = strings.Cut(c, ":")
	c = strings.TrimRight(c, " .")
	return strings.EqualFold(c, ".git") || strings.EqualFold(c, "git~1")
}

// hfsIgnorable are the code points HFS+ ignores in a name, as listed by git's next_hfs_char
// (utf8.c): zero-width (non-)joiners, direction marks and embeddings, the deprecated format
// characters U+206A–U+206F, and the zero-width no-break space.
var hfsIgnorable = map[rune]bool{
	0x200c: true, 0x200d: true, 0x200e: true, 0x200f: true,
	0x202a: true, 0x202b: true, 0x202c: true, 0x202d: true, 0x202e: true,
	0x206a: true, 0x206b: true, 0x206c: true, 0x206d: true, 0x206e: true, 0x206f: true,
	0xfeff: true,
}
