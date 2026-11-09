// Package faultfs is the crash harness for the failure-scenario suite (CLAUDE.md, "Testing
// conventions"; ADR-0003 §5). A Disk is an fs.Hook that:
//
//   - stops the "process" at a chosen step: that step and every later one fail with
//     ErrCrashed, so what is on disk afterwards is the state after a process kill;
//   - models what a power loss would leave: file data survives only once its file was
//     synced, and a directory entry (create, rename, mkdir) only once its directory was
//     synced. Unsynced data reverts to its last synced content (empty for a file never
//     synced) and unsynced entries are undone.
//
// The model is the conservative reading of POSIX. Real filesystems often keep more, so a
// sequence that passes here is safe on them too; the reverse isn't true.
package faultfs

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"maps"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// ErrCrashed is returned by the step a Disk crashes at and by every step after it.
var ErrCrashed = errors.New("faultfs: crashed")

// Never disables the crash: every step runs.
const Never = -1

type node struct {
	dir         bool
	data        []byte           // files: current content
	synced      []byte           // files: content that survives a power loss
	cur, stable map[string]*node // dirs: current entries, entries that survive a power loss
}

func newDir() *node { return &node{dir: true, cur: map[string]*node{}, stable: map[string]*node{}} }

// Disk models one vault directory. It is not safe for concurrent use.
type Disk struct {
	root    *node
	crashAt int
	steps   int
	crashed bool
}

// New snapshots dir as fully durable and crashes at step crashAt (0-based), or never.
func New(dir string, crashAt int) (*Disk, error) {
	root, err := load(os.DirFS(dir), ".")
	if err != nil {
		return nil, err
	}
	return &Disk{root: root, crashAt: crashAt}, nil
}

func load(fsys iofs.FS, path string) (*node, error) {
	n := newDir()
	entries, err := iofs.ReadDir(fsys, path)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		var child *node
		name := pathpkg.Join(path, e.Name())
		switch {
		case e.IsDir():
			child, err = load(fsys, name)
		case e.Type().IsRegular():
			var b []byte
			b, err = iofs.ReadFile(fsys, name)
			child = &node{data: b, synced: b}
		default:
			err = fmt.Errorf("faultfs: %s: only files and directories are modelled", name)
		}
		if err != nil {
			return nil, err
		}
		n.cur[e.Name()], n.stable[e.Name()] = child, child
	}
	return n, nil
}

// Steps is the number of steps that started, including the one that crashed. Running a
// sequence once with Never gives the number of crash points to test.
func (d *Disk) Steps() int { return d.steps }

// Crashed reports whether the crash happened.
func (d *Disk) Crashed() bool { return d.crashed }

// Before implements fs.Hook.
func (d *Disk) Before(fs.Op) error {
	if d.crashed {
		return ErrCrashed
	}
	if d.steps == d.crashAt {
		d.crashed = true
		d.steps++
		return ErrCrashed
	}
	d.steps++
	return nil
}

// After implements fs.Hook: a step that succeeded changes the model.
func (d *Disk) After(op fs.Op, err error) {
	if err != nil {
		return
	}
	switch op.Kind {
	case fs.OpCreate:
		parent, base := d.parent(op.Path)
		parent.cur[base] = &node{data: op.Data}
	case fs.OpSyncFile:
		n := d.lookup(op.Path)
		n.synced = n.data
	case fs.OpRename, fs.OpRenameNoReplace:
		fromParent, fromBase := d.parent(op.Path)
		toParent, toBase := d.parent(op.To)
		toParent.cur[toBase] = fromParent.cur[fromBase]
		delete(fromParent.cur, fromBase)
	case fs.OpMkdir:
		parent, base := d.parent(op.Path)
		parent.cur[base] = newDir()
	case fs.OpSyncDir:
		n := d.lookup(op.Path)
		n.stable = maps.Clone(n.cur)
	default:
		panic(fmt.Sprintf("faultfs: unmodelled step %v", op.Kind))
	}
}

// lookup follows current entries; the store only reports steps that succeeded on disk, so a
// missing path is a bug in the harness or the store, and panics.
func (d *Disk) lookup(rel string) *node {
	n := d.root
	if rel == "." {
		return n
	}
	for _, part := range strings.Split(rel, "/") {
		n = n.cur[part]
		if n == nil {
			panic(fmt.Sprintf("faultfs: %s is not in the model", rel))
		}
	}
	return n
}

func (d *Disk) parent(rel string) (*node, string) {
	dir, base := filepath.Split(rel)
	if dir == "" {
		return d.root, base
	}
	return d.lookup(strings.TrimSuffix(dir, "/")), base
}

// AfterPowerLoss returns what the vault would contain after a power loss now: each file's
// relative path mapped to its content, and each directory as "path/" mapped to nil.
func (d *Disk) AfterPowerLoss() map[string][]byte {
	out := map[string][]byte{}
	var walk func(n *node, prefix string)
	walk = func(n *node, prefix string) {
		for name, child := range n.stable {
			if child.dir {
				out[prefix+name+"/"] = nil
				walk(child, prefix+name+"/")
				continue
			}
			out[prefix+name] = child.synced
		}
	}
	walk(d.root, "")
	return out
}
