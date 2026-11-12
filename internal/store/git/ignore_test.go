package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIgnoreNoExponentialBacktracking: a hostile .gitignore can't stall Status. Without
// git's abort rule, `*a` x12 + `*b` against 60 a's runs for minutes; with it, microseconds.
// The 10 s guard only catches a hang (CLAUDE.md, Testing conventions: no wall-clock bound).
func TestIgnoreNoExponentialBacktracking(t *testing.T) {
	ign := NewIgnore(false).With([]byte(strings.Repeat("*a", 12)+"*b\n"+"x/"+strings.Repeat("**/a*", 8)+"b\n"), ".")
	matched := make(chan string, 1)
	go func() {
		for _, p := range []string{strings.Repeat("a", 60), "x/" + strings.Repeat("a/", 30) + "c"} {
			if ign.Match(p, false) {
				matched <- p
				return
			}
		}
		close(matched)
	}()
	select {
	case p, ok := <-matched:
		if ok {
			t.Fatalf("%q matched", p)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("matching did not finish within 10 s: exponential backtracking")
	}
}

// TestIgnoreBackslashes: names with a backslash never reach a commit (ADR-0006 refuses
// them), so the differential test can't cover these; the expectations are git 2.55's.
func TestIgnoreBackslashes(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		match   map[string]bool
	}{
		{`e\\ `, map[string]bool{`e\`: true, `e\ `: false, "e": false}}, // escaped backslash, then a trimmed space
		{`f\`, map[string]bool{`f\`: false, "f": false}},                // a lone trailing backslash matches nothing
		{`s\ `, map[string]bool{"s ": true, "s": false}},                // an escaped space is kept
	} {
		ign := NewIgnore(false).With([]byte(tc.pattern+"\n"), ".")
		for name, want := range tc.match {
			if got := ign.Match(name, false); got != want {
				t.Errorf("%q matches %q = %v, want %v", tc.pattern, name, got, want)
			}
		}
	}
}

// TestIgnoreCase reads core.ignorecase from the repository's config.
func TestIgnoreCase(t *testing.T) {
	for _, tc := range []struct {
		config string
		want   bool
	}{
		{"", false},
		{"[core]\n\tignorecase = true\n", true},
		{"[core]\n\tignoreCase = yes\n", true},
		{"[core]\n\tignorecase = 1\n", true},
		{"[core]\n\tignorecase\n", true},
		{"[core]\n\tignorecase = false\n", false},
	} {
		t.Run(tc.config, func(t *testing.T) {
			g, dir := newRepo(t)
			cfg := filepath.Join(dir, ".git", "config")
			b, err := os.ReadFile(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, append(b, tc.config...), 0o600); err != nil { //nolint:gosec // cfg is under t.TempDir()
				t.Fatal(err)
			}
			if got, err := g.IgnoreCase(); err != nil || got != tc.want {
				t.Fatalf("IgnoreCase = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
