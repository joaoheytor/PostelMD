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
type Ignore struct{ ps []ignorePattern }

type ignorePattern struct {
	base     string // directory of the file that holds it, "" for the root
	glob     string // the pattern, without `!`, the trailing `/` and a leading `/`
	negate   bool
	dirOnly  bool
	anchored bool // contained a slash: matched against the path below base, else the name
}

// With returns i plus the patterns of the ignore file b found in directory dir ("." for
// the vault root, and for .git/info/exclude). i is left unchanged; a nil i has no patterns.
func (i *Ignore) With(b []byte, dir string) *Ignore {
	var ps []ignorePattern
	if i != nil {
		ps = i.ps[:len(i.ps):len(i.ps)] // appending never writes into i's array
	}
	if dir == "." {
		dir = ""
	}
	for _, l := range strings.Split(string(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))), "\n") {
		if p, ok := parsePattern(strings.TrimSuffix(l, "\r"), dir); ok {
			ps = append(ps, p)
		}
	}
	return &Ignore{ps: ps}
}

func parsePattern(l, base string) (ignorePattern, bool) {
	if l == "" || l[0] == '#' {
		return ignorePattern{}, false
	}
	// Trailing spaces go, unless escaped with a backslash.
	for strings.HasSuffix(l, " ") && !strings.HasSuffix(l, `\ `) {
		l = l[:len(l)-1]
	}
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

// Match reports whether the vault path p (a directory if isDir) is ignored. It doesn't look
// at p's parents: the caller doesn't descend into an ignored directory.
func (i *Ignore) Match(p string, isDir bool) bool {
	if i == nil {
		return false
	}
	for k := len(i.ps) - 1; k >= 0; k-- {
		if i.ps[k].match(p, isDir) {
			return !i.ps[k].negate
		}
	}
	return false
}

func (pt *ignorePattern) match(p string, isDir bool) bool {
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
	return wild(pt.glob, rel, true)
}

// wild is git's wildmatch with WM_PATHNAME: `*`, `?` and classes never match `/`; `**`
// alone between slashes (or at either end) matches any number of directories; `[!…]` and
// `[^…]` negate a class; a backslash escapes the next character. afterSlash says p starts
// the pattern or follows a `/`.
func wild(p, t string, afterSlash bool) bool {
	for len(p) > 0 {
		c := p[0]
		switch {
		case c == '*':
			return matchStar(p, t, afterSlash)
		case len(t) == 0:
			return false
		case c == '\\':
			if len(p) < 2 || t[0] != p[1] {
				return false
			}
			p = p[1:]
		case c == '?':
			if t[0] == '/' {
				return false
			}
		case c == '[':
			n, ok := matchClass(p, t[0])
			if n == 0 || !ok || t[0] == '/' {
				return false
			}
			p = p[n-1:]
		case t[0] != c:
			return false
		}
		afterSlash = p[0] == '/'
		p, t = p[1:], t[1:]
	}
	return len(t) == 0
}

// matchStar matches p, which starts with `*`, against t.
func matchStar(p, t string, afterSlash bool) bool {
	rest := strings.TrimLeft(p, "*")
	globstar := len(p)-len(rest) >= 2 && afterSlash && (rest == "" || rest[0] == '/')
	switch {
	case globstar && rest == "": // a trailing `/**`, or `**` alone: everything
		return true
	case globstar: // `**/`: no directory, or any run of them
		for k := -1; k < len(t); k++ {
			if (k < 0 || t[k] == '/') && wild(rest[1:], t[k+1:], true) {
				return true
			}
		}
		return false
	case rest == "":
		return !strings.Contains(t, "/")
	}
	for k := 0; k <= len(t); k++ {
		if wild(rest, t[k:], false) {
			return true
		}
		if k < len(t) && t[k] == '/' {
			return false
		}
	}
	return false
}

// matchClass matches the bracket expression at the start of p against c. It returns the
// expression's length, or 0 if it has no closing bracket (git then fails the match).
func matchClass(p string, c byte) (int, bool) {
	k := 1
	negate := k < len(p) && (p[k] == '!' || p[k] == '^')
	if negate {
		k++
	}
	matched := false
	for first := true; k < len(p); first = false {
		if p[k] == ']' && !first {
			return k + 1, matched != negate
		}
		lo := p[k]
		if lo == '[' && k+1 < len(p) && p[k+1] == ':' {
			if end := strings.Index(p[k+2:], ":]"); end >= 0 {
				matched = matched || posixClass(p[k+2:k+2+end], c)
				k += end + 4
				continue
			}
		}
		if lo == '\\' && k+1 < len(p) {
			k++
			lo = p[k]
		}
		k++
		if k+1 < len(p) && p[k] == '-' && p[k+1] != ']' {
			hi := p[k+1]
			k += 2
			if hi == '\\' && k < len(p) {
				hi = p[k]
				k++
			}
			matched = matched || (lo <= c && c <= hi)
			continue
		}
		matched = matched || c == lo
	}
	return 0, false
}

func posixClass(name string, c byte) bool {
	switch name {
	case "alnum":
		return isAlpha(c) || isDigit(c)
	case "alpha":
		return isAlpha(c)
	case "digit":
		return isDigit(c)
	case "lower":
		return 'a' <= c && c <= 'z'
	case "upper":
		return 'A' <= c && c <= 'Z'
	case "space":
		return c == ' ' || ('\t' <= c && c <= '\r')
	case "xdigit":
		return isDigit(c) || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
	}
	return false
}

func isAlpha(c byte) bool { return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') }
func isDigit(c byte) bool { return '0' <= c && c <= '9' }
