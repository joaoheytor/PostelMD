//go:build linux

package http

import (
	"io"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentSavesWithTheSameETag is ADR-0003's race over HTTP: many saves sent together
// with the same If-Match. Exactly one wins (204), every other one is told it is stale (412),
// and the file holds the winner's body.
func TestConcurrentSavesWithTheSameETag(t *testing.T) {
	srv, dir := newAPI(t)
	const n = 12
	statuses, errs := make([]int, n), make([]error, n)
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		ready.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			<-start // released together
			statuses[i], errs[i] = put(srv.Client(), srv.URL+"/api/v1/notes/notes/a.md", "body "+strconv.Itoa(i))
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()

	won := -1
	for i, s := range statuses {
		switch {
		case errs[i] != nil:
			t.Fatalf("save %d: %v", i, errs[i])
		case s == 204 && won < 0:
			won = i
		case s == 204:
			t.Fatalf("saves %d and %d both succeeded with the same ETag", won, i)
		case s != 412:
			t.Fatalf("save %d = %d, want 204 or 412", i, s)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "notes", "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if won < 0 || string(b) != "body "+strconv.Itoa(won) {
		t.Fatalf("winner %d, file %q", won, b)
	}
}

// put saves body with notes/a.md's starting ETag; safe to call off the test goroutine.
func put(c *stdhttp.Client, url, body string) (int, error) {
	req, err := stdhttp.NewRequest("PUT", url, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("If-Match", revA())
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Body.Close()
}
