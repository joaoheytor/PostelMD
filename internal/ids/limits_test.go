//go:build linux

package ids

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A read never hashes a file over the size limit: the scan gives it its ID.
func TestSeenLeavesAFileOverTheLimitToTheScan(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	e.s.maxSize = 4
	e.write("big.md", "12345")
	if wait, err := e.s.Seen(ctx, "big.md"); err != nil || wait != 0 {
		t.Fatalf("Seen = %v, %v", wait, err)
	}
	e.noRow("big.md")
	e.pass()
	e.row("big.md")
}

// Concurrent reads of one unseen path run one single-path pass; the others wait for it and
// share its answer.
func TestConcurrentReadsOfOnePathRunOnePass(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	e.write("new.md", "new")
	var mu sync.Mutex
	planned := 0
	arrived := make(chan struct{}, 1) // the second read joined, or planned on its own
	second := make(chan error, 1)
	setKillPoint(t, func(p string) {
		mu.Lock()
		n := 0
		if p == "scan-planned" {
			planned++
			n = planned
		}
		mu.Unlock()
		switch {
		case p == "seen-joined" || n == 2:
			arrived <- struct{}{}
		case n == 1:
			go func() {
				_, err := e.s.Seen(ctx, "new.md")
				second <- err
			}()
			select {
			case <-arrived:
			case <-time.After(10 * time.Second): // hang guard
			}
		}
	})
	if wait, err := e.s.Seen(ctx, "new.md"); err != nil || wait != 0 {
		t.Fatalf("Seen = %v, %v", wait, err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if planned != 1 {
		t.Fatalf("%d passes planned for one path, want 1", planned)
	}
	e.row("new.md")
}

// A FIFO, a symlink to a file outside the vault and a symlinked folder are not files of the
// vault: no pass or read gives them rows, and none blocks on them.
func TestSpecialFilesGetNoRows(t *testing.T) {
	e := newEnv(t, map[string]string{"a.md": "A"})
	outside := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(outside, "dir"), 0o700))
	must(t, os.WriteFile(filepath.Join(outside, "o.md"), []byte("outside"), 0o600))
	must(t, os.WriteFile(filepath.Join(outside, "dir", "c.md"), []byte("outside"), 0o600))
	must(t, syscall.Mkfifo(filepath.Join(e.dir, "fifo.md"), 0o600))
	must(t, os.Symlink(filepath.Join(outside, "o.md"), filepath.Join(e.dir, "link.md")))
	must(t, os.Symlink(filepath.Join(outside, "dir"), filepath.Join(e.dir, "linked")))
	special := []string{"fifo.md", "link.md", "linked/c.md"}
	done := make(chan error, 1)
	go func() {
		if _, err := e.s.Pass(ctx); err != nil {
			done <- err
			return
		}
		for _, p := range special {
			if _, err := e.s.Seen(ctx, p); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("a pass or a read blocked on a special file")
	}
	for _, p := range special {
		e.noRow(p)
	}
	e.row("a.md")
}
