//go:build linux

package ids

import (
	"context"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// The scan leaves alone every path an open or flagged intent names, and everything under it
// (ADR-0033, "The scan and intents"): it never makes such a path's row suspect, tombstones
// or moves it, and never gives a path there an ID or a hold. Those paths are the intent's
// until recovery or the operator ends it. An intent whose payload doesn't decode (recovery
// flags it) names no path the scan can read, so it protects none.

// intentPaths returns whether a path is under one of the intents' roots.
func (s *Scanner) intentPaths(ctx context.Context) (func(string) bool, error) {
	in, err := s.db.Intents(ctx)
	if err != nil {
		return nil, err
	}
	var roots []string
	for _, i := range in {
		if r, err := i.Roots(); err == nil {
			roots = append(roots, r...)
		}
	}
	return func(p string) bool {
		for _, r := range roots {
			if sqlstore.Under(p, r) {
				return true
			}
		}
		return false
	}, nil
}

// leaveAlone drops from a pass's rows, holds and paths those an intent names. A row holding a
// path an intent names goes too, wherever it is: deciding it (a tombstone, say) would
// release that hold with it.
func leaveAlone(named func(string) bool, rows []sqlstore.File, holds []sqlstore.Pending, paths []string) (
	[]sqlstore.File, []sqlstore.Pending, []string) {
	keptHolds := holds[:0:0]
	holding := map[string]bool{}
	for _, h := range holds {
		if named(h.Path) {
			holding[h.FileID] = true
		} else {
			keptHolds = append(keptHolds, h)
		}
	}
	keptRows := rows[:0:0]
	for _, r := range rows {
		if !named(r.Path) && !holding[r.ID] {
			keptRows = append(keptRows, r)
		}
	}
	keptPaths := paths[:0:0]
	for _, p := range paths {
		if !named(p) {
			keptPaths = append(keptPaths, p)
		}
	}
	return keptRows, keptHolds, keptPaths
}
