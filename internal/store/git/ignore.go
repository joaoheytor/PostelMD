package git

import (
	"bytes"
	"strings"
)

// Ignore is a set of git ignore patterns, in increasing priority: .git/info/exclude first,
// then every .gitignore from the root down, each scoped to its directory. The last matching
// pattern wins and `!` re-includes, as in git (gitignore(5)). It only parses bytes; the
// caller reads the files through the vault's root, and never descends into an ignored
// directory, which is how git keeps a file under an excluded directory from being
// re-included. core.excludesFile is not read: the container has no global git config.
//
// go-git's matcher isn't used: it tests a pattern against every path component, so `!dir/`
// re-includes every file under dir, and its bracket classes don't negate with `!`.
type Ignore struct {
	ps   []ignorePattern
	fold bool // core.ignorecase
}

type ignorePattern struct {
	base     string // directory of the file that holds it, "" for the root
	glob     string // the pattern, without `!`, the trailing `/` and a leading `/`
	negate   bool
	dirOnly  bool
	anchored bool // contained a slash: matched against the path below base, else the name
}

// NewIgnore returns an empty set; fold is core.ignorecase (Repo.IgnoreCase).
func NewIgnore(fold bool) *Ignore { return &Ignore{fold: fold} }

// With returns i plus the patterns of the ignore file b found in directory dir ("." for
// the vault root, and for .git/info/exclude). i is left unchanged.
func (i *Ignore) With(b []byte, dir string) *Ignore {
	ps := i.ps[:len(i.ps):len(i.ps)] // appending never writes into i's array
	if dir == "." {
		dir = ""
	}
	for _, l := range strings.Split(string(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))), "\n") {
		if p, ok := parsePattern(strings.TrimSuffix(l, "\r"), dir); ok {
			ps = append(ps, p)
		}
	}
	return &Ignore{ps: ps, fold: i.fold}
}

func parsePattern(l, base string) (ignorePattern, bool) {
	if l == "" || l[0] == '#' {
		return ignorePattern{}, false
	}
	l = trimTrailingSpace(l)
	p := ignorePattern{base: base}
	if strings.HasPrefix(l, "!") {
		p.negate, l = true, l[1:]
	}
	if strings.HasSuffix(l, "/") {
		p.dirOnly, l = true, strings.TrimSuffix(l, "/")
	}
	p.anchored = strings.Contains(l, "/")
	p.glob = strings.TrimPrefix(l, "/")
	return p, p.glob != ""
}

// trimTrailingSpace is git's trim_trailing_spaces: it removes the trailing run of spaces,
// unless a backslash escapes it; a backslash escapes exactly the next character, and a
// pattern ending in a lone backslash is left as it is. A trailing tab is part of the pattern
// (git 2.55, checked by the differential test).
func trimTrailingSpace(l string) string {
	last := -1
	for k := 0; k < len(l); k++ {
		switch l[k] {
		case ' ':
			if last < 0 {
				last = k
			}
		case '\\':
			k++
			if k == len(l) {
				return l
			}
			last = -1
		default:
			last = -1
		}
	}
	if last >= 0 {
		return l[:last]
	}
	return l
}

// Match reports whether the vault path p (a directory if isDir) is ignored. It doesn't look
// at p's parents: the caller doesn't descend into an ignored directory.
func (i *Ignore) Match(p string, isDir bool) bool {
	for k := len(i.ps) - 1; k >= 0; k-- {
		if i.ps[k].match(p, isDir, i.fold) {
			return !i.ps[k].negate
		}
	}
	return false
}

func (pt *ignorePattern) match(p string, isDir, fold bool) bool {
	if pt.dirOnly && !isDir {
		return false
	}
	rel := p
	if pt.base != "" {
		if !strings.HasPrefix(p, pt.base+"/") {
			return false
		}
		rel = p[len(pt.base)+1:]
	}
	if !pt.anchored {
		rel = rel[strings.LastIndex(rel, "/")+1:]
	}
	return dowild(pt.glob, 0, rel, 0, fold) == wmMatch
}
