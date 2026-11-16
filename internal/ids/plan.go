//go:build linux

package ids

import (
	"bytes"
	"time"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// SecondPass is how long a path must stay missing before its ID is tombstoned or moved by
// rule 2 (ADR-0031: absence takes two passes, the second at least 10 s after the first).
const SecondPass = 10 * time.Second

// check is one path a unit depends on, as the plan saw it: present with that stat, or
// absent. Under the vault mutex, a unit whose checks no longer hold is dropped.
type check struct {
	path    string
	present bool
	stat    fs.Stat
}

// unit is one decision of a pass: the checks it rests on and the rows it writes, applied
// together or not at all.
type unit struct {
	checks  []check
	changes []sqlstore.Change
	count   *int // the Totals field it adds one to when applied
}

// Totals counts a pass's applied decisions, for ADR-0030's "scan totals" log record.
type Totals struct {
	NewIDs, Bumps, Moves, Suspects, Held, Tombstones int
}

// observed is a hashed file: its stat after the hash and the hash.
type observed struct {
	stat fs.Stat
	sum  []byte
}

// planner decides one pass from the rows, the holds and what is on disk (ADR-0031's scan
// and rename rules). It writes nothing. A full pass sees every path; a single-path pass (a
// read, only != "") decides only for that path and leaves every other row as it is.
type planner struct {
	now   time.Time
	rows  []sqlstore.File
	holds []sqlstore.Pending
	// stat is the plan's view of the disk; hash hashes a path (ok false: gone or busy).
	stat  func(p string) (fs.Stat, bool)
	hash  func(p string) (observed, bool)
	paths []string // the paths on disk: every file for a full pass, the one path for a read
	only  string

	totals *Totals
	units  []unit
	// done: the rows the plan has decided for (moved, held, back, busy), by ID; moved: the
	// ones whose ID moves; released: the holds let go, by path.
	done, moved, released map[string]bool
	claimed               map[string]bool // the paths an ID moved to, by path
	news                  []newPath       // the paths with no row and no hold standing
	held                  []heldPath      // the held paths whose suspect is still missing
	others                map[string]int  // the held paths the plan doesn't decide, by hash
	inserted              [][]byte        // the hashes of the paths given new IDs
	wait                  time.Duration   // for only: how long until its hold resolves
}

type newPath struct {
	path string
	obs  observed
}

// heldPath is a hold that still stands when the plan starts deciding: r is its suspect.
type heldPath struct {
	n newPath
	r *sqlstore.File
}

// plan decides in ADR-0036's order: the rows whose paths are there, then rule 1 over every
// new and held path (an inode that comes back beats a hold), then the holds that still
// stand (rule 2's uniqueness checked again) and rule 2 for the other new paths, then the
// rows still missing.
func (p *planner) plan() []unit {
	p.done, p.moved, p.released, p.claimed = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	p.others = map[string]int{}
	var missing []*sqlstore.File
	for i := range p.rows {
		r := &p.rows[i]
		st, on := p.stat(r.Path)
		switch {
		case !on:
			missing = append(missing, r)
		case p.only == "":
			p.present(r, st)
		}
	}
	p.findNew()
	p.checkHolds()
	p.rule1(missing)
	p.rule2(missing)
	if p.only == "" {
		p.resolveMissing(missing)
	}
	return p.units
}

// present decides for a row whose path is on disk: unchanged, changed (hashed again, and a
// new generation if the bytes differ), not hashed yet, or a suspect that is back.
func (p *planner) present(r *sqlstore.File, st fs.Stat) {
	same := sameStat(r.Stat, st) && r.SHA256 != nil
	if same && !r.Suspect {
		return
	}
	o := observed{stat: st, sum: r.SHA256}
	if !same {
		var ok bool
		if o, ok = p.hash(r.Path); !ok {
			return // gone or busy: the next pass decides
		}
	}
	bump := r.SHA256 != nil && !bytes.Equal(o.sum, r.SHA256)
	u := unit{checks: []check{{r.Path, true, o.stat}},
		changes: append(p.releaseHolds(r, ""), update(r, r.Path, o, bump))} // back: its held path gets a new ID
	if bump {
		u.count = &p.totals.Bumps
	}
	p.done[r.ID] = true
	p.units = append(p.units, u)
}

// findNew hashes every path on disk that has no row and no hold (or whose hold was
// released): the candidates for the rename rules.
func (p *planner) findNew() {
	known := map[string]bool{}
	for _, r := range p.rows {
		known[r.Path] = true
	}
	for _, h := range p.holds {
		known[h.Path] = !p.released[h.Path]
	}
	for _, path := range p.paths {
		if !known[path] {
			p.addNew(path)
		}
	}
}

func (p *planner) addNew(path string) {
	if o, ok := p.hash(path); ok {
		p.news = append(p.news, newPath{path, o})
	}
}

// checkHolds sorts the holds: a held path that went away is released; one whose suspect is
// back, or whose bytes changed, is released and taken as a new path; one that is busy waits
// for the next pass; the rest stand, for the rules to decide. A read decides only its own
// path's hold and counts the others, as recorded, for rule 2's uniqueness.
func (p *planner) checkHolds() {
	for _, h := range p.holds {
		r := p.row(h.FileID)
		if p.released[h.Path] || r == nil {
			continue // its suspect is back (present released it), or no row this pass reads
		}
		st, on := p.stat(h.Path)
		switch {
		case p.only != "" && h.Path != p.only:
			if on {
				p.others[string(h.SHA256)]++
			}
			continue
		case !on:
			p.units = append(p.units, unit{checks: []check{{h.Path, false, fs.Stat{}}},
				changes: []sqlstore.Change{sqlstore.Release{Path: h.Path}}})
			p.released[h.Path] = true
			continue
		}
		if _, back := p.stat(r.Path); back { // a read: the suspect is back, the next pass clears it
			p.released[h.Path] = true
			p.addNew(h.Path)
			continue
		}
		o := observed{stat: st, sum: h.SHA256}
		if !sameStat(h.Stat, st) {
			var ok bool
			if o, ok = p.hash(h.Path); !ok {
				p.done[r.ID] = true // busy: decide next pass
				p.others[string(h.SHA256)]++
				continue
			}
		}
		if !bytes.Equal(o.sum, h.SHA256) { // edited while held: no longer a rename
			p.released[h.Path] = true
			p.news = append(p.news, newPath{h.Path, o})
			continue
		}
		p.held = append(p.held, heldPath{newPath{h.Path, o}, r})
	}
}

func (p *planner) row(id string) *sqlstore.File {
	for i := range p.rows {
		if p.rows[i].ID == id {
			return &p.rows[i]
		}
	}
	return nil
}

// since is when r was first missed, as this pass counts it, and whether to record that: a
// time in the future (the system clock stepped back) counts from now, so no wait is longer
// than SecondPass (ADR-0036).
func (p *planner) since(r *sqlstore.File) (t time.Time, record bool) {
	if !r.Suspect || r.SuspectSince.After(p.now) {
		return p.now, true
	}
	return r.SuspectSince, false
}

func update(r *sqlstore.File, path string, o observed, bump bool) sqlstore.UpdateFile {
	return sqlstore.UpdateFile{Row: rowOf(r), NewPath: path, SHA256: o.sum, Stat: fileStat(o.stat), Bump: bump}
}

func rowOf(r *sqlstore.File) sqlstore.Row {
	return sqlstore.Row{ID: r.ID, Path: r.Path, Generation: r.Generation}
}

// sameStat is the scan's "unchanged" test: size, mtime, ctime, device and inode.
func sameStat(a sqlstore.FileStat, b fs.Stat) bool { return a == fileStat(b) }

func fileStat(s fs.Stat) sqlstore.FileStat {
	return sqlstore.FileStat{Size: s.Size, Device: s.Device, Inode: s.Inode, MtimeNS: s.MtimeNS, CtimeNS: s.CtimeNS}
}
