package auth

import (
	"sync"
	"time"
)

// backoff slows down password guessing per source IP, and never locks anyone out
// (ADR-0005, ADR-0020): after freeFailures, each attempt from that IP must wait twice as
// long as the last (1 s, doubling, capped). A success resets the IP. There is no
// account-wide backoff: with one account and no passkeys, that would let anyone delay the
// owner's login.
type backoff struct {
	mu  sync.Mutex
	ips map[string]*attempts
	now func() time.Time
}

type attempts struct {
	failures int
	next     time.Time // earliest allowed attempt
}

const (
	freeFailures = 5
	maxWait      = 15 * time.Minute
	maxTracked   = 10_000
	forgetAfter  = time.Hour
)

func newBackoff(now func() time.Time) *backoff {
	return &backoff{ips: map[string]*attempts{}, now: now}
}

// wait returns how long ip must wait before its next attempt (0 if it may try now).
func (b *backoff) wait(ip string) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if a := b.ips[ip]; a != nil {
		if d := a.next.Sub(b.now()); d > 0 {
			return d
		}
	}
	return 0
}

func (b *backoff) fail(ip string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if len(b.ips) >= maxTracked {
		for k, a := range b.ips {
			if now.Sub(a.next) > forgetAfter {
				delete(b.ips, k)
			}
		}
	}
	a := b.ips[ip]
	if a == nil {
		if len(b.ips) >= maxTracked {
			return // still full of active IPs: the per-attempt hashing cost remains
		}
		a = &attempts{}
		b.ips[ip] = a
	}
	a.failures++
	if n := a.failures - freeFailures; n > 0 {
		d := time.Second << min(n-1, 20)
		a.next = now.Add(min(d, maxWait))
	}
}

func (b *backoff) success(ip string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.ips, ip)
}
