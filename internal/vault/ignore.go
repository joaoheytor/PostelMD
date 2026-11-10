package vault

import (
	"errors"
	"strings"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/git"
)

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
	if errors.Is(err, git.ErrExcludeSkipped) {
		v.log.Warn("ignoring .git/info/exclude", "err", err)
	} else if err != nil {
		return err
	}
	w := &commitWalk{v: v, tracked: map[string]bool{}, fn: fn}
	for p := range committed {
		for d := p; strings.Contains(d, "/"); {
			d = d[:strings.LastIndex(d, "/")]
			w.tracked[d] = true
		}
	}
	var none *git.Ignore
	return w.walk(".", none.With(exclude, "."), false)
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
