package faultfs

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
)

// Materialize writes what the vault would contain after a power loss now into dir (created
// if absent, and empty), keeping file identity: a file reachable from two surviving entries
// is one file with two names (a hard link), as a power loss leaves it when the destination's
// folder of a rename was synced and the source's wasn't (ADR-0033's same-inode rows). Files
// are written 0600 and folders 0700. A folder reachable from two entries can't be linked: it
// is written at each, and the files under it are linked between the copies.
func (d *Disk) Materialize(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	first := map[*node]string{} // a file node -> where it was written first
	var walk func(n *node, at string) error
	walk = func(n *node, at string) error {
		for _, name := range slices.Sorted(maps.Keys(n.stable)) {
			child, p := n.stable[name], filepath.Join(at, name)
			var err error
			switch prev, linked := first[child]; {
			case child.dir:
				if err = os.Mkdir(p, 0o700); err == nil {
					err = walk(child, p)
				}
			case linked:
				err = os.Link(prev, p)
			default:
				first[child] = p
				err = os.WriteFile(p, child.synced, 0o600)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	return walk(d.root, dir)
}
