package vault

import (
	"errors"
	"fmt"
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
type commitWalk struct {
	v       *Vault
	tracked map[string]bool // directories holding committed files
	fn      func(p string, size int64, ignored bool) error
}

func (v *Vault) walkForCommit(committed map[string]git.Hash, fn func(p string, size int64, ignored bool) error) error {
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
	w := &commitWalk{v: v, tracked: map[string]bool{}, fn: fn}
	for p := range committed {
		for d := p; strings.Contains(d, "/"); {
			d = d[:strings.LastIndex(d, "/")]
			w.tracked[d] = true
		}
	}
	return w.walk(".", git.NewIgnore(fold).With(exclude, "."), false)
}

func (w *commitWalk) walk(dir string, ign *git.Ignore, ignored bool) error {
	entries, err := w.v.root.ReadDir(dir)
	if err != nil {
		return err
	}
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
			if err := w.walk(p, ign, isIgnored); err != nil {
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

func subPath(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}
