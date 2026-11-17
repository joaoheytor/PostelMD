//go:build linux

// Package ids keeps file identity in step with the vault on disk (ADR-0031, ADR-0036): the
// startup scan, a read's pass over one path, and the tombstones' expiry. It plans without
// the vault mutex (walk, stat, hash), then takes the mutex, re-checks every path a decision
// rests on, and commits the pass in one transaction (the vault mutex first, ADR-0030).
package ids

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/joaoheytor/PostelMD/internal/errattr"
	"github.com/joaoheytor/PostelMD/internal/store/fs"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// killPoint is a test hook: crash tests SIGKILL the process at a named point (CLAUDE.md rule
// 14), and the re-check test changes the vault there.
var killPoint = func(string) {}

// Scanner reconciles one vault with its rows in app.db.
type Scanner struct {
	db  *sqlstore.DB
	v   *vault.Vault
	log *slog.Logger
	now func() time.Time
	// secondAfter is SecondPass; expireEvery how often the expiry runs. Tests shorten them.
	secondAfter, expireEvery time.Duration
	maxSize                  int64 // the largest file a read hashes (fs.MaxFileSize)

	mu       sync.Mutex
	flights  map[string]*flight // the reads' passes in progress, by path
	failedAt time.Time          // when the last full pass failed; zero after one succeeds
}

// New returns a scanner of v's rows in db. The caller has checked that db's IDs describe v
// (the "assigned" flag, and the vault marker naming db's vault UUID).
func New(db *sqlstore.DB, v *vault.Vault, log *slog.Logger) *Scanner {
	return &Scanner{db: db, v: v, log: log, now: time.Now, secondAfter: SecondPass, expireEvery: time.Hour,
		maxSize: fs.MaxFileSize, flights: map[string]*flight{}}
}

// Pass runs one full pass: every file on disk against every row and hold. A walk that fails
// changes nothing (an unreadable folder is never taken for missing files); a file that can't
// be hashed is left for the next pass. A failure holds the reads' full passes back for
// FullPassRetry from when it failed; a success lets them run again. A read-only vault
// (ADR-0037) isn't walked: the pass couldn't commit.
func (s *Scanner) Pass(ctx context.Context) (Totals, error) {
	if s.v.ReadOnly() {
		return Totals{}, vault.ErrReadOnly
	}
	t, err := s.fullPass(ctx, s.now())
	switch {
	case err == nil:
		s.fullPassed()
	case ctx.Err() == nil:
		s.fullFailed(s.now())
	}
	return t, err
}

func (s *Scanner) fullPass(ctx context.Context, now time.Time) (Totals, error) {
	rows, err := s.db.Files(ctx)
	if err != nil {
		return Totals{}, err
	}
	holds, err := s.db.Holds(ctx)
	if err != nil {
		return Totals{}, err
	}
	killPoint("scan-walk")
	disk, err := s.v.Stats(ctx)
	if err != nil {
		return Totals{}, err
	}
	paths := make([]string, 0, len(disk))
	for p := range disk {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	t := &Totals{}
	p := &planner{now: now, rows: rows, holds: holds, paths: paths, totals: t, hash: s.hasher(ctx),
		stat: func(path string) (fs.Stat, bool) { st, ok := disk[path]; return st, ok }}
	err = s.apply(ctx, now, p.plan())
	return *t, err
}

// Seen is a read's pass over p, a path the scan may not have seen (ADR-0031): a path with
// no row and no hold gets the rename rules against every row with its hash or its inode,
// live or suspect, whose path is gone (its ID, a hold, or a new ID); a held path whose
// suspect is still missing gets the ID once SecondPass has passed since the suspect was
// first missed. It returns how long until a hold on p resolves (the read answers 503 with
// Retry-After), else 0. A path that isn't a regular file, is over the read size limit, or
// already has a row, is left to the read and the scan. A read sees one path, so a rule-2
// move it would make (the hold is due) runs a full pass instead, which sees every copy.
// Concurrent reads of one path share one pass, and concurrent full passes one full pass.
func (s *Scanner) Seen(ctx context.Context, p string) (time.Duration, error) {
	st, on, err := s.v.StatPath(p)
	if err != nil || !on || st.Size > s.maxSize {
		return 0, err
	}
	return s.once(ctx, p, func() (time.Duration, error) { return s.seen(ctx, p) })
}

// fullPass is a read's full pass; its key "" is never a path.
const fullPass = ""

// once runs fn for key, or, while a call for key runs, waits for it and shares its wait.
func (s *Scanner) once(ctx context.Context, key string, fn func() (time.Duration, error)) (time.Duration, error) {
	s.mu.Lock()
	if f, ok := s.flights[key]; ok {
		s.mu.Unlock()
		killPoint("seen-joined")
		select {
		case <-f.done:
			return f.wait, nil // the first caller reports its error
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	s.flights[key] = f
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.flights, key)
		s.mu.Unlock()
		close(f.done)
	}()
	var err error
	f.wait, err = fn()
	return f.wait, err
}

// flight is a read's pass in progress, which the concurrent reads of the same key wait for.
type flight struct {
	done chan struct{}
	wait time.Duration
}

func (s *Scanner) seen(ctx context.Context, p string) (time.Duration, error) {
	if _, err := s.db.FileByPath(ctx, p); !errors.Is(err, sqlstore.ErrNoFile) {
		return 0, err
	}
	hash := s.hasher(ctx)
	o, ok := hash(p)
	if !ok {
		return 0, nil // gone or busy: the scan decides
	}
	now := s.now()
	rows, err := s.db.FilesMatching(ctx, o.sum, o.stat.Device, o.stat.Inode)
	if err != nil {
		return 0, err
	}
	holds, err := s.db.Holds(ctx)
	if err != nil {
		return 0, err
	}
	pl := &planner{now: now, rows: rows, holds: holds, paths: []string{p}, only: p, totals: &Totals{},
		hash: func(path string) (observed, bool) {
			if path == p {
				return o, true
			}
			return hash(path)
		},
		stat: func(path string) (fs.Stat, bool) {
			st, ok, err := s.v.StatPath(path)
			return st, ok && err == nil
		}}
	units := pl.plan()
	if pl.full && s.fullRetryWaits() {
		return 0, nil // a full pass failed lately: the read goes on, as after that failure (#184)
	}
	if pl.full {
		return s.once(ctx, fullPass, func() (time.Duration, error) { _, err := s.Pass(ctx); return 0, err })
	}
	if err := s.apply(ctx, now, units); err != nil {
		return 0, err
	}
	return pl.wait, nil
}

// hasher hashes a path for the planner; a file that went away, a busy writer or an error
// reading it (logged without its path) leaves it for the next pass.
func (s *Scanner) hasher(ctx context.Context) func(string) (observed, bool) {
	return func(p string) (observed, bool) {
		o, ok, err := s.v.Observe(ctx, p)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("the scan couldn't hash a file; the next pass tries again", errattr.Pathless(err))
			}
			return observed{}, false
		}
		if !ok || o.SHA256 == nil {
			return observed{}, false
		}
		return observed{stat: o.Stat, sum: o.SHA256[:]}, true
	}
}

// apply takes the vault mutex, drops every unit whose paths changed since the plan, and
// commits the rest in one transaction, counting what was applied.
func (s *Scanner) apply(ctx context.Context, now time.Time, units []unit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(units) == 0 {
		return nil
	}
	killPoint("scan-planned")
	return s.v.Locked(ctx, func() error {
		var keep []unit
		var batch [][]sqlstore.Change
		for _, u := range units {
			ok, err := s.holds(u.checks)
			if err != nil {
				return err
			}
			if ok {
				keep, batch = append(keep, u), append(batch, u.changes)
			}
		}
		if len(batch) == 0 {
			return nil
		}
		applied, err := s.db.ApplyScan(ctx, now, batch)
		if err != nil {
			return err
		}
		for i, ok := range applied {
			if ok && keep[i].count != nil {
				*keep[i].count++
			}
		}
		return nil
	})
}

// holds reports whether every check of a unit still holds on disk.
func (s *Scanner) holds(checks []check) (bool, error) {
	for _, c := range checks {
		st, on, err := s.v.StatPath(c.path)
		if err != nil {
			return false, err
		}
		if on != c.present || on && st != c.stat {
			return false, nil
		}
	}
	return true, nil
}
