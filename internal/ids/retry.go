//go:build linux

package ids

import "time"

// FullPassRetry is how long after a failed full pass a read of a due hold waits before it
// runs another one (#184, ADR-0037). Until then the read is served without it, as after a
// failed pass (ADR-0036), so a walk that keeps failing (an unreadable folder) runs at most
// once a minute, not once per read.
const FullPassRetry = time.Minute

// fullFailed records that a full pass failed at now (the pass's time, on the scanner's clock).
func (s *Scanner) fullFailed(now time.Time) {
	s.mu.Lock()
	s.failedAt = now
	s.mu.Unlock()
}

// fullPassed clears a failure: the next due hold's read runs a full pass at once.
func (s *Scanner) fullPassed() {
	s.mu.Lock()
	s.failedAt = time.Time{}
	s.mu.Unlock()
}

// fullRetryWaits reports whether a full pass failed less than FullPassRetry ago. A failure
// time in the future (the clock stepped back) counts as now, so the wait is never longer.
func (s *Scanner) fullRetryWaits() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failedAt.IsZero() {
		return false
	}
	now := s.now()
	if s.failedAt.After(now) {
		s.failedAt = now
	}
	return now.Sub(s.failedAt) < FullPassRetry
}
