package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// indexLock is git's lock for .git/index.
const indexLock = "index.lock"

// ErrIndexLocked: .git/index.lock exists. Another git process holds it, or a crash left it
// behind; like a ref lock, only the user may remove it (ADR-0020).
var ErrIndexLocked = errors.New("store/git: the index is locked")

// ResetIndex makes .git/index list commit's tree, so the user's own git sees the working
// tree as PostelMD committed it (ADR-0016: a commit resets the index to HEAD), except the
// paths checkPath refuses (ADR-0028: they stay in the tree, out of the index). The
// entries carry no stat data, so git re-hashes each file once and then shows it clean. It
// uses git's lockfile protocol: index.lock, fsync, rename, fsync the directory.
func (g *Repo) ResetIndex(commit Hash) error {
	idx := &index.Index{Version: 2}
	if !commit.IsZero() {
		c, err := g.r.CommitObject(commit)
		if err != nil {
			return fmt.Errorf("store/git: index: %w", err)
		}
		t, err := c.Tree()
		if err != nil {
			return fmt.Errorf("store/git: index: %w", err)
		}
		// The whole tree is walked, names go-git refuses included, so one such name (a
		// git~1 committed on the host) never cuts the index short. Names checkPath refuses
		// are left out: the user's git then shows each as one staged deletion. So is an entry
		// whose own name holds a "/" (only a crafted tree has one): it would name a path
		// twice, and git refuses an index with a duplicate entry.
		w := object.NewTreeWalker(t, true, nil)
		defer w.Close()
		w.SkipPathValidation()
		for {
			name, e, err := w.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("store/git: index: %w", err)
			}
			if e.Mode != filemode.Dir && !strings.Contains(e.Name, "/") && checkPath(name) == nil {
				idx.Entries = append(idx.Entries, &index.Entry{Name: name, Hash: e.Hash, Mode: e.Mode})
			}
		}
	}
	sort.Slice(idx.Entries, func(i, j int) bool { return idx.Entries[i].Name < idx.Entries[j].Name })
	var buf bytes.Buffer
	if err := index.NewEncoder(&buf).Encode(idx); err != nil {
		return fmt.Errorf("store/git: encode index: %w", err)
	}
	if err := g.dotgit.CreateFile(indexLock, buf.Bytes(), 0o644); err != nil {
		if errors.Is(err, iofs.ErrExist) {
			return ErrIndexLocked
		}
		return fmt.Errorf("store/git: lock index: %w", err)
	}
	if err := g.dotgit.SyncFile(indexLock); err != nil {
		return fmt.Errorf("store/git: index: %w", err)
	}
	if err := g.dotgit.Rename(indexLock, "index"); err != nil {
		return fmt.Errorf("store/git: index: %w", err)
	}
	return g.dotgit.SyncDir(".")
}
