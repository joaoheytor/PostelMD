//go:build linux

package vault

import (
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ignoreCases are trees whose untracked, non-ignored files PostelMD must list exactly as
// the host's git does. Every file not named ".gitignore" holds "x".
type ignoreCase struct {
	name    string
	ignores map[string]string // .gitignore paths (or ".git/info/exclude") to content
	files   []string
}

var ignoreCases = []ignoreCase{
	{"names, dirs, negation, nested scope",
		map[string]string{".gitignore": ".env\nsecrets/\n*.log\n!keep.log\n# c\n\n", "sub/.gitignore": "local.md\n"},
		[]string{".env", "secrets/k.md", "secrets.md", "x.log", "deep/z.log", "keep.log", "sub/local.md", "sub/n.md", "local.md"}},
	{"a negated directory doesn't re-include its files",
		map[string]string{".gitignore": "*.env\n!config/\n"},
		[]string{"config/prod.env", "config/a.md", "b.env"}},
	{"whitelist idiom",
		map[string]string{".gitignore": "*\n!*/\n!*.md\n"},
		[]string{"docs/secret.txt", "docs/a.md", "a.md", "b.txt", "x/y/z.md", "x/y/z.txt"}},
	{"negated bracket class !", map[string]string{".gitignore": "*.[!m]d\n"}, []string{"a.xd", "a.md", "notes/a.md", "b.ad"}},
	{"negated bracket class ^", map[string]string{".gitignore": "[^a]*.txt\n"}, []string{"a.txt", "b.txt", "d/ab.txt", "d/ba.txt"}},
	{"bracket range and set", map[string]string{".gitignore": "[a-c].md\n[xz]?.md\n"}, []string{"a.md", "c.md", "d.md", "xy.md", "zz.md", "yy.md"}},
	{"trailing /** is the contents only",
		map[string]string{".gitignore": "foo/**\n!foo/keep.md\n"},
		[]string{"foo/keep.md", "foo/x.md", "foo/sub/y.md", "foo.md"}},
	{"a nested .gitignore re-includes",
		map[string]string{".gitignore": "*.log\n", "sub/.gitignore": "!keep.log\n"},
		[]string{"sub/keep.log", "sub/other.log", "keep.log"}},
	{"no re-include inside an excluded directory",
		map[string]string{".gitignore": "build/\n", "build/.gitignore": "!x\n"},
		[]string{"build/x", "build/y", "x"}},
	{"a/*/b", map[string]string{".gitignore": "a/*/b\n"}, []string{"a/x/b", "a/x/y/b", "a/b", "c/a/x/b"}},
	{"a slash anchors", map[string]string{".gitignore": "docs/*.md\n"}, []string{"docs/a.md", "docs/sub/a.md", "a.md", "x/docs/a.md"}},
	{"a nested slash anchors to its directory",
		map[string]string{"sub/.gitignore": "/x.md\ny/z.md\n"},
		[]string{"sub/x.md", "x.md", "sub/y/x.md", "sub/y/z.md", "y/z.md"}},
	{"info/exclude below .gitignore",
		map[string]string{".git/info/exclude": "*.tmp\n!x.log\n", ".gitignore": "!keep.tmp\n*.log\n"},
		[]string{"a.tmp", "keep.tmp", "x.log", "d/b.tmp"}},
	{"leading **/", map[string]string{".gitignore": "**/logs\n**/cache/*.bin\n"},
		[]string{"logs/a", "x/logs/b", "x/logsy", "cache/a.bin", "q/cache/b.bin", "q/cache/c.txt"}},
	{"a/**/b", map[string]string{".gitignore": "a/**/b\n"}, []string{"a/b", "a/x/b", "a/x/y/b", "ab", "c/a/b"}},
	{"leading slash", map[string]string{".gitignore": "/root.md\n"}, []string{"root.md", "sub/root.md"}},
	{"trailing spaces and escapes",
		map[string]string{".gitignore": "trail.md   \nsp\\ \n\\#hash\n\\!bang\n"},
		[]string{"trail.md", "sp ", "sp", "#hash", "!bang"}},
	{"directory-only vs a file", map[string]string{".gitignore": "doc/\n"}, []string{"doc", "x/doc/a", "y/doc"}},
	{"a name matches a directory too", map[string]string{".gitignore": "build\n"}, []string{"build/a", "x/build/b", "builds/c"}},
	{"? and **", map[string]string{".gitignore": "?.md\na**b\n"}, []string{"a.md", "ab.md", "axyb", "a/b", "q/a/zb"}},
	{"node_modules", map[string]string{".gitignore": "node_modules/\n"}, []string{"node_modules/p/i.js", "a/node_modules/x.js", "src/i.js"}},
	{"last match wins", map[string]string{".gitignore": "!a.md\n*.md\n!b.md\n"}, []string{"a.md", "b.md", "c.md"}},
	{"BOM and CRLF", map[string]string{".gitignore": "\xef\xbb\xbfbom.md\ncrlf.md\r\nplain.md\n"}, []string{"bom.md", "crlf.md", "plain.md", "z.md"}},
	{"trailing /** on a directory pattern and its parent", map[string]string{".gitignore": "a/b/**\n!a/b/c/\n"}, []string{"a/b/c/d", "a/b/e", "a/x"}},
	{"**/x/** and x/**/*.md", map[string]string{".gitignore": "**/tmp/**\ndocs/**/*.md\n"},
		[]string{"tmp/a", "a/tmp/b/c", "tmpx/a", "docs/a.md", "docs/x/y/b.md", "docs/c.txt"}},
	{"POSIX class and escaped star", map[string]string{".gitignore": "[[:digit:]]*.md\n\\*.txt\n"},
		[]string{"1a.md", "a1.md", "*.txt", "a.txt"}},
	{"**/ ending in a slash, and a lone /", map[string]string{".gitignore": "a/**/\n/\n"}, []string{"a/b/c", "a/d", "b/a/x"}},
	{"case matters", map[string]string{".gitignore": "*.MD\n"}, []string{"a.md", "b.MD"}},
	{"a nested negation of a parent's directory pattern",
		map[string]string{".gitignore": "logs/\n", "app/.gitignore": "!logs/\n"},
		[]string{"logs/a", "app/logs/b", "app/x/logs/c"}},
	{"star doesn't cross a slash", map[string]string{".gitignore": "a/*.md\n"}, []string{"a/x.md", "a/b/x.md"}},
	// Git trims trailing spaces only: a trailing tab is part of the pattern.
	{"trailing tab", map[string]string{".gitignore": ".env\t\nb.md \t \nc.md\t \n"}, []string{".env", "b.md", "c.md", "c.md\t", "b.md \t"}},
	{"escaped trailing tab and space", map[string]string{".gitignore": "t\\\t\ns\\ \n"}, []string{"t\t", "t", "s ", "s"}},
	{"POSIX classes",
		map[string]string{".gitignore": "[[:blank:]]b.md\n[[:punct:]]p.md\n[[:graph:]]g.md\n[[:print:]]r.md\n[[:cntrl:]]c.md\n[[:space:]]s.md\n"},
		[]string{" b.md", "ab.md", "_p.md", "ap.md", "-p.md", "ag.md", " g.md", " r.md", "ar.md", "ac.md", " s.md", "as.md"}},
	{"an unknown or unclosed POSIX class", map[string]string{".gitignore": "[[:nope:]]n.md\n[[:alpha:]m.md\n"},
		[]string{"an.md", "[n.md", ":n.md", "am.md", "[m.md"}},
	{"brackets: ] first, ranges, escapes", map[string]string{".gitignore": "[]a]1\n[a-]2\n[\\]]3\n[!]]4\n"},
		[]string{"]1", "a1", "b1", "-2", "a2", "]3", "a3", "]4", "b4"}},
	{"* and ? stay within one directory", map[string]string{".gitignore": "*/x\n?/y\n"}, []string{"a/x", "a/b/x", "b/y", "bb/y"}},
}

// ignoreCaseCases run with core.ignorecase=true.
var ignoreCaseCases = []ignoreCase{
	{"names fold", map[string]string{".gitignore": "*.MD\nDocs/\nReadMe\n"}, []string{"a.md", "b.MD", "docs/a", "readme", "README"}},
	{"classes fold", map[string]string{".gitignore": "[A-C].txt\n[B]x\n[[:upper:]]u\n[[:lower:]]l\n[!a]n\n"},
		[]string{"a.txt", "B.txt", "d.txt", "bx", "Bx", "au", "Au", "al", "Al", "An", "bn"}},
	{"negation folds", map[string]string{".gitignore": "*.log\n!KEEP.log\n"}, []string{"keep.log", "x.LOG"}},
}

// TestIgnoreMatchesGit: for every case, Status's untracked files are exactly
// `git ls-files --others --exclude-standard`.
func TestIgnoreMatchesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	for _, tc := range ignoreCases {
		t.Run(tc.name, func(t *testing.T) { checkAgainstGit(t, tc, false) })
	}
	for _, tc := range ignoreCaseCases {
		t.Run("ignorecase/"+tc.name, func(t *testing.T) { checkAgainstGit(t, tc, true) })
	}
}

func checkAgainstGit(t *testing.T, tc ignoreCase, fold bool) {
	dir := t.TempDir()
	gitCLI(t, dir, "init", "-q", "-b", "main")
	if fold {
		gitCLI(t, dir, "config", "core.ignorecase", "true")
	}
	for _, f := range tc.files {
		writeFiles(t, dir, map[string]string{f: "x"})
	}
	writeFiles(t, dir, tc.ignores)
	v, err := Open(dir)
	must(t, err)
	defer func() { _ = v.Close() }()
	_, err = v.EnableGit()
	must(t, err)
	got := paths(changes(t, v))
	// Not gitCLI: its TrimSpace would eat a leading space of the first name.
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z", "--others", "--exclude-standard").Output()
	must(t, err)
	want := strings.Split(string(out), "\x00")
	want = want[:len(want)-1]
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PostelMD lists %q\n          git lists %q", got, want)
	}
}
