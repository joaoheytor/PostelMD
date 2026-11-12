package vault

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"strings"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/git"
)

// ErrUnsafeExclude: .git/info/exclude is a symlink or not a regular file, so PostelMD can't
// tell what git would ignore; status and commit refuse until the user replaces it.
var ErrUnsafeExclude = errors.New("vault: .git/info/exclude is a symlink or not a regular file; " +
	"PostelMD can't read it safely, replace it with a regular file")

// commitWalk is walk for commits: it applies git's ignore rules (git.Ignore), so what
// PostelMD commits is what `git add -A` would.
//
// An ignored directory isn't descended into (node_modules), unless it holds tracked files:
// a pattern never untracks a committed file, so fn sees those, flagged ignored, and the
// caller still commits their changes. A .gitignore is read only if it is a regular file,
// through the confined root, so a symlinked one is never followed.
//
// A folder with a .git of its own (a folder in a clone, a file in a submodule) is a nested
// repository: git add -A records a gitlink there, never the files inside, so the walk doesn't
// descend into it; it reports it to nested, flagged ignored or not (ADR-0026). An existing
// gitlink is never listed by git.Files, so commits leave it as it is.
type commitWalk struct {
	v       *Vault
	tracked map[string]bool // directories holding committed files
	fn      func(p string, size int64, ignored bool) error
	nested  func(p string, ignored bool)
	fold    bool // core.ignorecase: git folds case, as on a case-insensitive filesystem
}

func (v *Vault) walkForCommit(committed map[string]git.Hash, fn func(p string, size int64, ignored bool) error,
	nested func(p string, ignored bool)) error {
	exclude, err := v.git.Exclude()
	if errors.Is(err, git.ErrExcludeNotRegular) {
		// Git would follow it and apply its patterns; reading past it could commit what
		// the user excluded, and following it could leave the vault. Fail closed.
		return fmt.Errorf("%w: %w", ErrUnsafeExclude, err)
	}
	if err != nil {
		return err
	}
	fold, err := v.git.IgnoreCase()
	if err != nil {
		return err
	}
	w := &commitWalk{v: v, tracked: map[string]bool{}, fn: fn, nested: nested, fold: fold}
	for p := range committed {
		for d := p; strings.Contains(d, "/"); {
			d = d[:strings.LastIndex(d, "/")]
			w.tracked[d] = true
		}
	}
	entries, err := v.root.ReadDir(".")
	if err != nil {
		return err
	}
	return w.walk(".", entries, git.NewIgnore(fold).With(exclude, "."), false)
}

// hasGit reports whether a folder's entries include a .git of any kind (a folder in a clone,
// a file in a submodule). Git opens "<folder>/.git", so the name is matched exactly on a
// case-sensitive filesystem (".GIT" doesn't make a repository) and in any case when git
// folds case (fold, core.ignorecase). Other look-alikes, such as ".git ", are reserved but
// never make a repository: Linux keeps their spaces, dots and ignorable code points.
func hasGit(entries []iofs.DirEntry, fold bool) bool {
	for _, e := range entries {
		if e.Name() == ".git" || fold && strings.EqualFold(e.Name(), ".git") {
			return true
		}
	}
	return false
}

func (w *commitWalk) walk(dir string, entries []iofs.DirEntry, ign *git.Ignore, ignored bool) error {
	if !ignored {
		for _, e := range entries {
			if e.Name() == ".gitignore" && e.Type().IsRegular() {
				b, err := w.v.root.ReadFile(subPath(dir, ".gitignore"))
				if err != nil {
					return err
				}
				ign = ign.With(b, dir)
			}
		}
	}
	for _, e := range entries {
		p := subPath(dir, e.Name())
		if fs.IsReserved(p) {
			continue
		}
		isIgnored := ignored || ign.Match(p, e.IsDir())
		switch {
		case e.IsDir():
			if isIgnored && !w.tracked[p] {
				continue
			}
			sub, err := w.v.root.ReadDir(p)
			if err != nil {
				return err
			}
			if hasGit(sub, w.fold) {
				w.nested(p, isIgnored)
				continue
			}
			if err := w.walk(p, sub, ign, isIgnored); err != nil {
				return err
			}
		case e.Type().IsRegular():
			info, err := e.Info()
			if err != nil {
				return err
			}
			if err := w.fn(p, info.Size(), isIgnored); err != nil {
				return err
			}
		}
	}
	return nil
}

// walk calls fn for every regular file under dir that isn't reserved. Symlinks and other
// special files are skipped, never followed. Unlike commitWalk, it applies no ignore rules
// and descends into nested repositories: the finder lists every note.
func (v *Vault) walk(dir string, fn func(p string, size int64) error) error {
	entries, err := v.root.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := subPath(dir, e.Name())
		if fs.IsReserved(p) {
			continue
		}
		switch {
		case e.IsDir():
			if err := v.walk(p, fn); err != nil {
				return err
			}
		case e.Type().IsRegular():
			info, err := e.Info()
			if err != nil {
				return err
			}
			if err := fn(p, info.Size()); err != nil {
				return err
			}
		}
	}
	return nil
}

func subPath(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}
