package git

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

var (
	// ErrInvalidPath: a change path is not clean and relative, or names a .git component.
	ErrInvalidPath = errors.New("store/git: invalid path")
	// ErrTypeConflict: a change writes a file where the tree has a directory, or the reverse.
	ErrTypeConflict = errors.New("store/git: file/directory conflict")
)

// Commit writes a commit whose tree is parent's tree with changes applied: path -> new
// bytes, or nil to delete (deleting a missing path is a no-op, so recovery can replay it).
// A zero parent starts from an empty tree. No ref moves; see SetBranch.
func (g *Repo) Commit(parent Hash, changes map[string][]byte, msg string, author, committer Signature) (Hash, error) {
	for p := range changes {
		if err := checkPath(p); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("%w: %q", err, p)
		}
	}
	var root Hash
	if !parent.IsZero() {
		pc, err := g.r.CommitObject(parent)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("store/git: parent: %w", err)
		}
		root = pc.TreeHash
	}
	tree, err := g.applyTree(root, changes)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	c := &object.Commit{Author: author, Committer: committer, Message: msg, TreeHash: tree}
	if !parent.IsZero() {
		c.ParentHashes = []Hash{parent}
	}
	return g.store(c.Encode)
}

// checkPath accepts clean, relative, slash-separated paths with no ".git" component in any
// letter case, nor a name Windows or macOS treat as ".git" (fs.IsDotGit; git itself refuses
// such trees on checkout).
func checkPath(p string) error {
	if p == "" || strings.Contains(p, `\`) || strings.ContainsRune(p, 0) ||
		!filepath.IsLocal(p) || filepath.Clean(p) != p {
		return ErrInvalidPath
	}
	for _, part := range strings.Split(p, "/") {
		if fs.IsDotGit(part) {
			return ErrInvalidPath
		}
	}
	return nil
}

// applyTree returns the hash of tree with changes (paths relative to it) applied. An empty
// result is written only at the root; a subdirectory left empty is dropped by the caller.
func (g *Repo) applyTree(tree Hash, changes map[string][]byte) (Hash, error) {
	entries := map[string]object.TreeEntry{}
	if !tree.IsZero() {
		t, err := g.r.TreeObject(tree)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("store/git: tree: %w", err)
		}
		for _, e := range t.Entries {
			entries[e.Name] = e
		}
	}
	sub := map[string]map[string][]byte{}
	for p, data := range changes {
		dir, rest, nested := strings.Cut(p, "/")
		if nested {
			if sub[dir] == nil {
				sub[dir] = map[string][]byte{}
			}
			sub[dir][rest] = data
			continue
		}
		if err := g.applyFile(entries, p, data); err != nil {
			return plumbing.ZeroHash, err
		}
	}
	for dir, ch := range sub {
		if err := g.applyDir(entries, dir, ch); err != nil {
			return plumbing.ZeroHash, err
		}
	}
	return g.writeTree(entries)
}

func (g *Repo) applyFile(entries map[string]object.TreeEntry, name string, data []byte) error {
	old, exists := entries[name]
	if exists && old.Mode == filemode.Dir {
		return fmt.Errorf("%w: %s is a directory", ErrTypeConflict, name)
	}
	if data == nil {
		delete(entries, name)
		return nil
	}
	h, err := g.store(func(o plumbing.EncodedObject) error {
		o.SetType(plumbing.BlobObject)
		w, err := o.Writer()
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		return w.Close()
	})
	if err != nil {
		return err
	}
	mode := filemode.Regular
	if exists && old.Mode == filemode.Executable {
		mode = filemode.Executable
	}
	entries[name] = object.TreeEntry{Name: name, Mode: mode, Hash: h}
	return nil
}

func (g *Repo) applyDir(entries map[string]object.TreeEntry, name string, changes map[string][]byte) error {
	var subtree Hash
	if old, exists := entries[name]; exists {
		if old.Mode != filemode.Dir {
			return fmt.Errorf("%w: %s is a file", ErrTypeConflict, name)
		}
		subtree = old.Hash
	}
	h, err := g.applyTree(subtree, changes)
	if err != nil {
		return err
	}
	if t, err := g.r.TreeObject(h); err == nil && len(t.Entries) == 0 {
		delete(entries, name) // git has no empty directories
		return nil
	}
	entries[name] = object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h}
	return nil
}

// writeTree stores entries in git's canonical order: by name, with directories compared as
// if they ended in "/". go-git encodes entries in the order given.
func (g *Repo) writeTree(entries map[string]object.TreeEntry) (Hash, error) {
	t := &object.Tree{}
	for _, e := range entries {
		t.Entries = append(t.Entries, e)
	}
	key := func(e object.TreeEntry) string {
		if e.Mode == filemode.Dir {
			return e.Name + "/"
		}
		return e.Name
	}
	sort.Slice(t.Entries, func(i, j int) bool { return key(t.Entries[i]) < key(t.Entries[j]) })
	return g.store(t.Encode)
}

// store encodes an object with enc and writes it to the object database.
func (g *Repo) store(enc func(plumbing.EncodedObject) error) (Hash, error) {
	o := g.r.Storer.NewEncodedObject()
	if err := enc(o); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store/git: encode: %w", err)
	}
	h, err := g.r.Storer.SetEncodedObject(o)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store/git: write object: %w", err)
	}
	return h, nil
}
