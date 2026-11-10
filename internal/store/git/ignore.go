package git

import (
	"bytes"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// Ignore is a set of git ignore patterns, in increasing priority: .git/info/exclude first,
// then every .gitignore from the root down, each scoped to its directory. The last matching
// pattern wins and `!` re-includes, as in git. It only parses bytes; the caller reads the
// files through the vault's root. core.excludesFile is not read: the container has no
// global git config.
type Ignore struct{ ps []gitignore.Pattern }

// With returns i plus the patterns of the ignore file b found in directory dir ("." for
// the vault root, and for .git/info/exclude). i is left unchanged; a nil i has no patterns.
func (i *Ignore) With(b []byte, dir string) *Ignore {
	var ps []gitignore.Pattern
	if i != nil {
		ps = i.ps[:len(i.ps):len(i.ps)] // appending never writes into i's array
	}
	domain := split(dir)
	for _, l := range strings.Split(string(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))), "\n") {
		l = strings.TrimSuffix(l, "\r")
		if strings.HasPrefix(l, "#") || strings.TrimSpace(l) == "" {
			continue
		}
		ps = append(ps, gitignore.ParsePattern(l, domain))
	}
	return &Ignore{ps: ps}
}

// Match reports whether the vault path p (a directory if isDir) is ignored. It doesn't look
// at p's parents: the caller doesn't descend into an ignored directory.
func (i *Ignore) Match(p string, isDir bool) bool {
	if i == nil {
		return false
	}
	return gitignore.NewMatcher(i.ps).Match(split(p), isDir)
}

func split(p string) []string {
	if p == "." || p == "" {
		return nil
	}
	return strings.Split(p, "/")
}
