package git

import "strings"

// A port of git's wildmatch.c (dowild) with WM_PATHNAME always set: `*`, `?` and classes
// never match `/`; `**` alone between slashes (or at either end) matches any number of
// directories; `[!…]` and `[^…]` negate a class; a backslash escapes the next character.
// fold is WM_CASEFOLD (core.ignorecase), including git's own quirks: the text is folded,
// while a literal character inside a class is not.
//
// The abort results are git's: they stop a failed `*` from retrying at every later
// position, which keeps hostile patterns such as `*a*a*a…*b` linear instead of exponential.

type wmResult int

const (
	wmNoMatch wmResult = iota
	wmMatch
	wmAbortAll
	wmAbortToStarstar
)

// at returns s[i], or 0 past the end (git's NUL terminator).
func at(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func lower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

func dowild(p string, pi int, t string, ti int, fold bool) wmResult {
	for ; pi < len(p); pi, ti = pi+1, ti+1 {
		pc := p[pi]
		tc := at(t, ti)
		if tc == 0 && pc != '*' {
			return wmAbortAll
		}
		if fold {
			tc, pc = lower(tc), lower(pc)
		}
		switch pc {
		case '\\':
			pi++
			if pc = at(p, pi); tc != pc { // git doesn't fold an escaped character
				return wmNoMatch
			}
		case '?':
			if tc == '/' {
				return wmNoMatch
			}
		case '*':
			r, done, nti := star(p, pi, t, ti, fold)
			if done {
				return r
			}
			ti, pi = nti, pi+starLen(p, pi) // on the `/` of `*/` in both: the loop steps past them
		case '[':
			n, r := class(p, pi, tc, fold)
			if r != wmMatch {
				return r
			}
			pi = n
		default:
			if tc != pc {
				return wmNoMatch
			}
		}
	}
	if ti < len(t) {
		return wmNoMatch
	}
	return wmMatch
}

// starLen is the number of `*` at p[pi:].
func starLen(p string, pi int) int {
	n := 0
	for at(p, pi+n) == '*' {
		n++
	}
	return n
}

// star handles the `*` run at p[pi]. It returns the result and true, or false and the
// text position of the next slash when a single `*` is followed by `/` (the caller goes on
// matching from there).
func star(p string, pi int, t string, ti int, fold bool) (wmResult, bool, int) {
	n := starLen(p, pi)
	rest := pi + n
	matchSlash := false
	if n >= 2 {
		if (pi == 0 || p[pi-1] == '/') && (rest == len(p) || p[rest] == '/' || (p[rest] == '\\' && at(p, rest+1) == '/')) {
			if at(p, rest) == '/' && dowild(p, rest+1, t, ti, fold) == wmMatch {
				return wmMatch, true, 0
			}
			matchSlash = true
		}
	}
	if rest == len(p) {
		// A trailing `**` matches everything; a trailing `*` only if no slash is left.
		if !matchSlash && strings.IndexByte(t[ti:], '/') >= 0 {
			return wmNoMatch, true, 0
		}
		return wmMatch, true, 0
	}
	if !matchSlash && p[rest] == '/' {
		// A single `*` followed by a slash matches the rest of this directory name.
		k := strings.IndexByte(t[ti:], '/')
		if k < 0 {
			return wmNoMatch, true, 0
		}
		return 0, false, ti + k
	}
	for ti < len(t) {
		// Skip ahead to the next occurrence of a literal that follows the star.
		if pc := p[rest]; !strings.ContainsRune(`*?[\`, rune(pc)) {
			if fold {
				pc = lower(pc)
			}
			var tc byte
			for ; ti < len(t); ti++ {
				tc = t[ti]
				if !matchSlash && tc == '/' {
					break
				}
				if fold {
					tc = lower(tc)
				}
				if tc == pc {
					break
				}
			}
			if ti == len(t) || tc != pc {
				return wmNoMatch, true, 0
			}
		}
		if r := dowild(p, rest, t, ti, fold); r != wmNoMatch {
			if !matchSlash || r != wmAbortToStarstar {
				return r, true, 0
			}
		} else if !matchSlash && t[ti] == '/' {
			return wmAbortToStarstar, true, 0
		}
		ti++
	}
	return wmAbortAll, true, 0
}

// class matches the bracket expression at p[pi] against tc (already folded when fold). It
// returns the index of the closing `]` and wmMatch, or the failure.
func class(p string, pi int, tc byte, fold bool) (int, wmResult) {
	pi++
	pc := at(p, pi)
	if pc == '^' {
		pc = '!'
	}
	negated := pc == '!'
	if negated {
		pi++
		pc = at(p, pi)
	}
	var prev byte
	matched := false
	for {
		if pc == 0 {
			return pi, wmAbortAll
		}
		switch {
		case pc == '\\':
			pi++
			pc = at(p, pi)
			if pc == 0 {
				return pi, wmAbortAll
			}
			if tc == pc {
				matched = true
			}
		case pc == '-' && prev != 0 && at(p, pi+1) != 0 && at(p, pi+1) != ']':
			pi++
			pc = p[pi]
			if pc == '\\' {
				pi++
				pc = at(p, pi)
				if pc == 0 {
					return pi, wmAbortAll
				}
			}
			if prev <= tc && tc <= pc {
				matched = true
			} else if fold && 'a' <= tc && tc <= 'z' {
				if up := tc - 'a' + 'A'; prev <= up && up <= pc {
					matched = true
				}
			}
			pc = 0 // so prev becomes 0
		case pc == '[' && at(p, pi+1) == ':':
			s := pi + 2
			e := s
			for at(p, e) != 0 && p[e] != ']' {
				e++
			}
			if at(p, e) == 0 {
				return e, wmAbortAll
			}
			if e-s-1 < 0 || p[e-1] != ':' {
				// No ":]": a plain '[' in the set.
				if tc == '[' {
					matched = true
				}
				pc = '['
				break
			}
			ok, known := posixClass(p[s:e-1], tc, fold)
			if !known {
				return e, wmAbortAll
			}
			matched = matched || ok
			pi = e
			pc = 0
		default:
			if tc == pc {
				matched = true
			}
		}
		prev = pc
		pi++
		pc = at(p, pi)
		if pc == ']' {
			break
		}
	}
	if matched == negated || tc == '/' {
		return pi, wmNoMatch
	}
	return pi, wmMatch
}

// posixClass reports whether c is in the [:name:] class (ASCII, as git defines them), and
// whether the class exists.
func posixClass(name string, c byte, fold bool) (bool, bool) {
	isUpper, isLower := 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z'
	isDigit := '0' <= c && c <= '9'
	isAlpha := isUpper || isLower
	isSpace := c == ' ' || c == '\t' || c == '\n' || c == '\r'
	isPrint := 0x20 <= c && c <= 0x7e
	switch name {
	case "alnum":
		return isAlpha || isDigit, true
	case "alpha":
		return isAlpha, true
	case "blank":
		return c == ' ' || c == '\t', true
	case "cntrl":
		return c < 0x20 || c == 0x7f, true
	case "digit":
		return isDigit, true
	case "graph":
		return isPrint && c != ' ', true
	case "lower":
		return isLower, true
	case "print":
		return isPrint, true
	case "punct":
		return isPrint && c != ' ' && !isAlpha && !isDigit, true
	case "space":
		return isSpace, true
	case "upper":
		return isUpper || (fold && isLower), true
	case "xdigit":
		return isDigit || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F'), true
	}
	return false, false
}
