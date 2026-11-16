//go:build linux

package ids

import (
	"context"
	"sync"
	"time"

	"github.com/joaoheytor/PostelMD/internal/errattr"
)

// TombstoneTTL is how long an ended ID answers 410 before it is forgotten (ADR-0031).
const TombstoneTTL = 30 * 24 * time.Hour

// Start runs the startup scan's first pass (ADR-0030's step 7), then, in the background,
// the second pass SecondPass later, for the suspects and held paths the first one found,
// and the tombstones' expiry, at once and every hour. A failed pass is logged and changes
// nothing; serving goes on (no ID is on the wire yet, ADR-0031). Start returns ctx's error
// if ctx ends during the first pass. stop cancels the background work and waits for it.
func (s *Scanner) Start(ctx context.Context) (stop func(), err error) {
	s.pass(ctx, 1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	killPoint("scan-first-pass-done")
	bg, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTimer(s.secondAfter)
		defer t.Stop()
		select {
		case <-bg.Done():
		case <-t.C:
			s.pass(bg, 2)
		}
	})
	wg.Go(func() { s.expire(bg) })
	return func() { cancel(); wg.Wait() }, nil
}

// pass runs one pass and logs its totals (ADR-0030's structured record; no path).
func (s *Scanner) pass(ctx context.Context, n int) {
	start := time.Now()
	t, err := s.Pass(ctx)
	switch {
	case err != nil && ctx.Err() != nil:
		return // stopping
	case err != nil:
		s.log.Error("the startup scan failed; file IDs are as they were", "pass", n, errattr.Pathless(err))
		return
	}
	s.log.Info("startup scan", "pass", n, "new_ids", t.NewIDs, "generation_bumps", t.Bumps, "moved_ids", t.Moves,
		"suspects", t.Suspects, "held", t.Held, "tombstones", t.Tombstones, "duration_ms", time.Since(start).Milliseconds())
}

// expire removes the tombstones older than TombstoneTTL now and every expireEvery until ctx
// ends. It never takes the vault mutex (ADR-0030's background writers).
func (s *Scanner) expire(ctx context.Context) {
	t := time.NewTicker(s.expireEvery)
	defer t.Stop()
	for {
		n, err := s.db.ExpireTombstones(ctx, s.now().Add(-TombstoneTTL))
		switch {
		case err != nil && ctx.Err() == nil:
			s.log.Warn("couldn't remove expired tombstones; the next run tries again", errattr.Pathless(err))
		case n > 0:
			s.log.Info("expired tombstones removed", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
