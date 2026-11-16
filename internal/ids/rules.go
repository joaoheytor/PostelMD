//go:build linux

package ids

import (
	"bytes"
	"time"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// ADR-0031's rename rules, as ADR-0036 makes them exact, against the rows whose paths are
// missing:
//   - rule 1 (mv): the same hash, device, inode and mtime as exactly one missing row: the
//     ID moves at once, to a new path or to a held one, and the row's own hold, if any, is
//     let go (its path gets a new ID);
//   - rule 2 (copy and delete): the same hash and another inode, the only missing row and
//     the only new or held path with that hash: the path is held until the row has been
//     missing SecondPass, then the ID moves. A hold is provisional: rule 1 can take its row,
//     and before it becomes a move its uniqueness is checked again; if that fails, the hold
//     is let go, the path gets a new ID, and the row is tombstoned as ambiguous-rename once
//     it has been missing SecondPass;
//   - anything else: a new ID.

// rule1 moves an ID to each new or held path that is its file, moved.
func (p *planner) rule1(missing []*sqlstore.File) {
	// The missing rows by inode: a folder moved outside makes as many new paths as missing
	// rows, so the rule looks rows up instead of comparing every pair.
	byInode := map[[2]uint64][]*sqlstore.File{}
	for _, r := range missing {
		k := [2]uint64{r.Stat.Device, r.Stat.Inode}
		byInode[k] = append(byInode[k], r)
	}
	candidates := append([]newPath{}, p.news...)
	for _, h := range p.held {
		candidates = append(candidates, h.n)
	}
	for _, n := range candidates {
		var c []*sqlstore.File
		for _, r := range byInode[[2]uint64{n.obs.stat.Device, n.obs.stat.Inode}] {
			if !p.done[r.ID] && bytes.Equal(r.SHA256, n.obs.sum) && r.Stat.MtimeNS == n.obs.stat.MtimeNS {
				c = append(c, r)
			}
		}
		if len(c) == 1 {
			p.move(c[0], n)
		}
	}
}

// rule2 decides for the holds that still stand, then for the new paths rule 1 left.
func (p *planner) rule2(missing []*sqlstore.File) {
	byHash := map[string][]*sqlstore.File{}
	for _, r := range missing {
		if r.SHA256 != nil {
			byHash[string(r.SHA256)] = append(byHash[string(r.SHA256)], r)
		}
	}
	var rest []newPath
	for _, n := range p.news {
		if !p.claimed[n.path] {
			rest = append(rest, n)
		}
	}
	var holding []heldPath
	for _, h := range p.held {
		switch {
		case p.claimed[h.n.path]:
		case p.released[h.n.path]: // its row moved by rule 1
			rest = append(rest, h.n)
		default:
			holding = append(holding, h)
		}
	}
	paths := map[string]int{} // the new and held paths by hash
	for _, n := range rest {
		paths[string(n.obs.sum)]++
	}
	for _, h := range holding {
		paths[string(h.n.obs.sum)]++
	}
	for sum, n := range p.others {
		paths[sum] += n
	}
	for _, h := range holding {
		if p.only1(byHash[string(h.n.obs.sum)], paths[string(h.n.obs.sum)]) != h.r {
			p.released[h.n.path] = true // ambiguous now: a new ID; resolveMissing tombstones the row
			rest = append(rest, h.n)
			continue
		}
		p.hold(h.r, h.n)
	}
	for _, n := range rest {
		if r := p.only1(byHash[string(n.obs.sum)], paths[string(n.obs.sum)]); r != nil && !p.done[r.ID] && !sameInode(r, n) {
			p.hold(r, n)
			continue
		}
		p.units = append(p.units, unit{
			checks:  []check{{n.path, true, n.obs.stat}},
			changes: append(p.release(n.path), sqlstore.InsertFile{Path: n.path, SHA256: n.obs.sum, Stat: fileStat(n.obs.stat)}),
			count:   &p.totals.NewIDs,
		})
		p.inserted = append(p.inserted, n.obs.sum)
	}
}

// only1 returns the one missing row (of those with a path's hash, whose IDs haven't moved)
// when that path is also the only new or held path with the hash, else nil.
func (p *planner) only1(sameHash []*sqlstore.File, paths int) *sqlstore.File {
	var rows []*sqlstore.File
	for _, r := range sameHash {
		if !p.moved[r.ID] {
			rows = append(rows, r)
		}
	}
	if paths != 1 || len(rows) != 1 {
		return nil
	}
	return rows[0]
}

// move gives r's ID to the path n now (rule 1, or rule 2 once the row has been missing
// SecondPass). A hold on n is dropped with it, and so is any other path r held.
func (p *planner) move(r *sqlstore.File, n newPath) {
	p.done[r.ID], p.moved[r.ID], p.claimed[n.path] = true, true, true
	changes := p.releaseHolds(r, n.path)
	if p.held1(n.path) {
		p.released[n.path] = true
	}
	p.units = append(p.units, unit{
		checks:  []check{{r.Path, false, fs.Stat{}}, {n.path, true, n.obs.stat}},
		changes: append(append(changes, p.release(n.path)...), update(r, n.path, n.obs, true)),
		count:   &p.totals.Moves,
	})
}

// hold records a rule-2 match, or keeps one: the row is suspect from now (if it isn't yet,
// or its clock is in the future) and n waits for it, unless it has been missing SecondPass
// already.
func (p *planner) hold(r *sqlstore.File, n newPath) {
	since, record := p.since(r)
	left := since.Add(SecondPass).Sub(p.now)
	if left <= 0 {
		p.move(r, n)
		return
	}
	p.done[r.ID] = true
	if n.path == p.only {
		p.wait = left
	}
	var changes []sqlstore.Change
	if record {
		changes = append(changes, sqlstore.Suspect{Row: rowOf(r)})
	}
	if p.held1(n.path) && !p.released[n.path] { // already held for r: only a clock to restart, if any
		if record {
			p.units = append(p.units, unit{checks: []check{{r.Path, false, fs.Stat{}}}, changes: changes})
		}
		return
	}
	changes = append(changes, p.release(n.path)...)
	changes = append(changes, sqlstore.Hold{Path: n.path, FileID: r.ID, SHA256: n.obs.sum, Stat: fileStat(n.obs.stat)})
	p.units = append(p.units, unit{
		checks:  []check{{r.Path, false, fs.Stat{}}, {n.path, true, n.obs.stat}},
		changes: changes,
		count:   &p.totals.Held,
	})
}

// resolveMissing decides for the rows still missing after the rules (a full pass only): a
// live row becomes suspect; a suspect missing for SecondPass is tombstoned, as
// ambiguous-rename when a path with its bytes got an ID since it went missing, else missing.
func (p *planner) resolveMissing(missing []*sqlstore.File) {
	// When PostelMD first saw each path with a hash (an ID's ULID time), by hash and ID; ""
	// for the paths given IDs in this pass.
	seen := map[string]map[string]time.Time{}
	add := func(sum []byte, id string, t time.Time) {
		if seen[string(sum)] == nil {
			seen[string(sum)] = map[string]time.Time{}
		}
		seen[string(sum)][id] = t
	}
	for _, o := range p.rows {
		if o.SHA256 != nil {
			add(o.SHA256, o.ID, sqlstore.IDTime(o.ID))
		}
	}
	for _, sum := range p.inserted {
		add(sum, "", p.now)
	}
	for _, r := range missing {
		if p.done[r.ID] {
			continue
		}
		absent := []check{{r.Path, false, fs.Stat{}}}
		since, record := p.since(r)
		switch {
		case !r.Suspect:
			p.units = append(p.units, unit{checks: absent, changes: []sqlstore.Change{sqlstore.Suspect{Row: rowOf(r)}},
				count: &p.totals.Suspects})
		case record: // its clock is in the future: it restarts now
			p.units = append(p.units, unit{checks: absent, changes: []sqlstore.Change{sqlstore.Suspect{Row: rowOf(r)}}})
		case !p.now.Before(since.Add(SecondPass)):
			// Another path with its bytes got an ID since it went missing: a rename the
			// rules couldn't match to one row.
			reason := "missing"
			for id, t := range seen[string(r.SHA256)] {
				if r.SHA256 != nil && id != r.ID && !t.Before(r.SuspectSince) {
					reason = "ambiguous-rename"
				}
			}
			p.units = append(p.units, unit{checks: absent,
				changes: []sqlstore.Change{sqlstore.TombstoneFile{Row: rowOf(r), Reason: reason}},
				count:   &p.totals.Tombstones})
		}
	}
}

// releaseHolds lets go every path r holds but keep: the Release changes, which go in the
// unit that decides r.
func (p *planner) releaseHolds(r *sqlstore.File, keep string) []sqlstore.Change {
	var out []sqlstore.Change
	for _, h := range p.holds {
		if h.FileID == r.ID && h.Path != keep {
			out = append(out, sqlstore.Release{Path: h.Path})
			p.released[h.Path] = true
		}
	}
	return out
}

// held1 reports whether path is held, by any row.
func (p *planner) held1(path string) bool {
	for _, h := range p.holds {
		if h.Path == path {
			return true
		}
	}
	return false
}

// release is the Release of path when its hold was released this pass, so the new row and
// the hold's removal land together.
func (p *planner) release(path string) []sqlstore.Change {
	if p.released[path] {
		return []sqlstore.Change{sqlstore.Release{Path: path}}
	}
	return nil
}

func sameInode(r *sqlstore.File, n newPath) bool {
	return r.Stat.Device == n.obs.stat.Device && r.Stat.Inode == n.obs.stat.Inode
}
