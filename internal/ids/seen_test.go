//go:build linux

package ids

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// A read of a path the scan hasn't seen gives it an ID; of one with a row, changes nothing.
func TestSeenGivesAnUnseenPathItsID(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	e.write("new.md", "new")
	if wait, err := e.s.Seen(ctx, "new.md"); err != nil || wait != 0 {
		t.Fatalf("Seen = %v, %v", wait, err)
	}
	r := e.row("new.md")
	e.write("a.md", "edited, left to the scan")
	for _, p := range []string{"new.md", "a.md", "missing.md", ".postelmd-tmp/x", "../escape"} {
		if wait, err := e.s.Seen(ctx, p); err != nil || wait != 0 {
			t.Fatalf("Seen(%q) = %v, %v", p, wait, err)
		}
	}
	if again := e.row("new.md"); again.ID != r.ID || e.row("a.md").Generation != 1 {
		t.Fatal("a read of a path with a row changed it")
	}
}

// A read of a held path before the 10 s answers with the time left, and resolves the match
// after (the roadmap's 503 with Retry-After). A read can also make the hold.
func TestSeenOfAHeldPathWaitsThenResolves(t *testing.T) {
	for _, held := range []string{"by the scan", "by the read"} {
		t.Run(held, func(t *testing.T) {
			e := newEnv(t, map[string]string{"a.md": "unique"})
			id := e.row("a.md").ID
			// Out of the vault but not deleted: a freed inode could be reused by the copy,
			// which is TestRenameRulesOnTheSignals' "cp over the freed inode" (a new ID).
			e.aside("a.md")
			e.pass() // a.md suspect
			e.cp2("unique", "copy.md")
			if held == "by the scan" {
				e.after(time.Second)
				e.pass()
			}
			e.after(3 * time.Second)
			wait, err := e.s.Seen(ctx, "copy.md")
			want := SecondPass - 3*time.Second
			if held == "by the scan" {
				want -= time.Second
			}
			if err != nil || wait != want {
				t.Fatalf("Seen before the 10 s = %v, %v; want %v", wait, err, want)
			}
			e.noRow("copy.md")
			e.after(wait)
			if wait, err := e.s.Seen(ctx, "copy.md"); err != nil || wait != 0 {
				t.Fatalf("Seen after = %v, %v", wait, err)
			}
			if r := e.row("copy.md"); r.ID != id || r.Generation != 2 {
				t.Fatalf("copy.md: %+v, want a.md's ID", r)
			}
		})
	}
}

// The decisions rest on the paths as the plan saw them: a file back at its path before the
// vault mutex is taken keeps its ID, and the new path gets nothing until the next pass.
func TestAPathThatChangesBeforeTheMutexIsLeftForTheNextPass(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "same"})
	id := e.row("a.md").ID
	e.mv("a.md", "b.md")
	setKillPoint(t, func(p string) {
		if p == "scan-planned" {
			e.cp("b.md", "a.md")
		}
	})
	if got := e.pass(); got != (Totals{}) {
		t.Fatalf("totals %+v, want nothing applied", got)
	}
	if r := e.row("a.md"); r.ID != id || r.Suspect {
		t.Fatalf("a.md: %+v", r)
	}
	e.noRow("b.md")
}

func TestStartRunsTheSecondPassAndExpiresTombstones(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A", "b.md": "B", "c.md": "C"})
	logs := &logBuffer{}
	e.s.log = slog.New(slog.NewJSONHandler(logs, nil))
	e.s.secondAfter, e.s.expireEvery = 10*time.Millisecond, 10*time.Millisecond
	old, recent := e.row("b.md").ID, e.row("c.md").ID
	e.rm("b.md")
	e.pass()
	e.after(SecondPass)
	e.pass() // b.md tombstoned at the clock's time
	e.rm("c.md")
	e.pass()
	e.after(SecondPass)
	e.pass() // c.md tombstoned SecondPass later
	e.rm("a.md")
	id := e.row("a.md").ID
	// The expiry sees b.md's tombstone as TTL + 5 s old, c.md's as TTL - 5 s.
	e.s.now = func() time.Time { return e.clock.Add(TombstoneTTL - 5*time.Second) }
	stop, err := e.s.Start(ctx)
	must(t, err)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logs.String(), `"msg":"startup scan","pass":2`) ||
		!strings.Contains(logs.String(), "expired tombstones removed") {
		if time.Now().After(deadline) {
			t.Fatalf("no second pass or expiry: %s", logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Fatalf("a pass failed: %s", logs.String())
	}
	if _, err := e.db.TombstoneByID(ctx, old); err == nil {
		t.Fatal("the expired tombstone is still there")
	}
	e.tombstone(recent) // younger than TombstoneTTL: kept
	// The first pass made a.md suspect at the shifted clock; the second, 10 ms later on the
	// same clock, left it: absence takes 10 s, not two passes.
	if r := e.row("a.md"); r.ID != id || !r.Suspect {
		t.Fatalf("a.md: %+v, want suspect", r)
	}
	if strings.Contains(logs.String(), "a.md") || strings.Contains(logs.String(), e.dir) {
		t.Fatalf("a log record names a path: %s", logs.String())
	}
}

// stop ends the background work on its own, with the parent context still alive.
func TestStopEndsTheBackgroundWork(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	e.s.secondAfter = time.Hour
	stop, err := e.s.Start(ctx)
	must(t, err)
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the background work didn't stop")
	}
}

// logBuffer is a log destination the background passes write while the test reads it.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// cp2 writes content to a new file p: a copy made by a tool, with a new inode.
func (e *env) cp2(content, p string) { e.write(p, content) }

func setKillPoint(t *testing.T, fn func(string)) {
	t.Helper()
	old := killPoint
	killPoint = fn
	t.Cleanup(func() { killPoint = old })
}
