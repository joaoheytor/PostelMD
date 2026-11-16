//go:build linux

package ids

import (
	"bytes"
	"time"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// matchNew applies ADR-0031's rename rules to the new paths, against the rows whose paths
// are missing:
//   - rule 1 (mv): the same hash, device, inode and mtime as exactly one missing row: the
//     ID moves at once;
//   - rule 2 (copy and delete): the same hash and another inode, the only missing row and
//     the only new or held path with that hash: the path is held until the row has been
//     missing SecondPass, then the ID moves (at once if it already has);
//   - anything else: a new ID.
func (p *planner) matchNew(missing []*sqlstore.File) {
	open := func(r *sqlstore.File) bool { return !p.done[r.ID] }
	// The missing rows by inode and by hash: a folder moved outside makes as many new paths
	// as missing rows, so the rules look rows up instead of comparing every pair.
	byInode, byHash := map[[2]uint64][]*sqlstore.File{}, map[string][]*sqlstore.File{}
	for _, r := range missing {
		byInode[[2]uint64{r.Stat.Device, r.Stat.Inode}] = append(byInode[[2]uint64{r.Stat.Device, r.Stat.Inode}], r)
		if r.SHA256 != nil {
			byHash[string(r.SHA256)] = append(byHash[string(r.SHA256)], r)
		}
	}
	var rest []newPath
	for _, n := range p.news {
		var c []*sqlstore.File
		for _, r := range byInode[[2]uint64{n.obs.stat.Device, n.obs.stat.Inode}] {
			if open(r) && bytes.Equal(r.SHA256, n.obs.sum) && r.Stat.MtimeNS == n.obs.stat.MtimeNS {
				c = append(c, r)
			}
		}
		if len(c) == 1 {
			p.move(c[0], n)
			continue
		}
		rest = append(rest, n)
	}
	paths := map[string]int{} // the new and held paths by hash
	for _, n := range rest {
		paths[string(n.obs.sum)]++
	}
	for _, h := range p.holds {
		if !p.released[h.Path] {
			paths[string(h.SHA256)]++
		}
	}
	for _, n := range rest {
		if r := p.rule2(byHash[string(n.obs.sum)], paths[string(n.obs.sum)]); r != nil && open(r) && !sameInode(r, n) {
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

// rule2 returns the one missing row (of those with a new path's hash, whose IDs haven't
// moved) when that path is also the only new or held path with the hash, else nil.
func (p *planner) rule2(sameHash []*sqlstore.File, paths int) *sqlstore.File {
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

// move gives r's ID to the new path n now (rule 1, or rule 2 once the row has been missing
// SecondPass).
func (p *planner) move(r *sqlstore.File, n newPath) {
	p.done[r.ID], p.moved[r.ID], p.claimed[n.path] = true, true, true
	p.units = append(p.units, unit{
		checks:  []check{{r.Path, false, fs.Stat{}}, {n.path, true, n.obs.stat}},
		changes: append(p.release(n.path), update(r, n.path, n.obs, true)),
		count:   &p.totals.Moves,
	})
}

// hold records a rule-2 match: the row is suspect from now (if it isn't yet) and n waits for
// it, unless it has been missing SecondPass already.
func (p *planner) hold(r *sqlstore.File, n newPath) {
	since := p.now
	if r.Suspect {
		since = r.SuspectSince
	}
	left := since.Add(SecondPass).Sub(p.now)
	if left <= 0 {
		p.move(r, n)
		return
	}
	p.done[r.ID] = true
	var changes []sqlstore.Change
	if !r.Suspect {
		changes = append(changes, sqlstore.Suspect{Row: rowOf(r)})
	}
	changes = append(changes, p.release(n.path)...)
	changes = append(changes, sqlstore.Hold{Path: n.path, FileID: r.ID, SHA256: n.obs.sum, Stat: fileStat(n.obs.stat)})
	p.units = append(p.units, unit{
		checks:  []check{{r.Path, false, fs.Stat{}}, {n.path, true, n.obs.stat}},
		changes: changes,
		count:   &p.totals.Held,
	})
	if n.path == p.only {
		p.wait = left
	}
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
		switch {
		case !r.Suspect:
			p.units = append(p.units, unit{checks: absent, changes: []sqlstore.Change{sqlstore.Suspect{Row: rowOf(r)}},
				count: &p.totals.Suspects})
		case !p.now.Before(r.SuspectSince.Add(SecondPass)):
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
