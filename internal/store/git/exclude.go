package git

import (
	"bytes"
	"errors"
	"fmt"
	iofs "io/fs"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

const (
	excludeFile = "info/exclude"
	excludeNew  = excludeFile + ".postelmd-new" // ours alone: a leftover is removed
)

// reservedExcludes keep PostelMD's own folders out of the user's git (`git add -A`).
var reservedExcludes = []string{"/.postelmd-trash/", "/.postelmd-tmp/"}

// ErrExcludeSkipped: .git/info/exclude is a symlink or not a regular file, so it is neither
// followed nor rewritten, and its patterns don't apply. Callers warn and go on.
var ErrExcludeSkipped = errors.New("store/git: info/exclude isn't a regular file; its patterns are not applied")

// Exclude returns .git/info/exclude, or nil if there is none. A symlink isn't followed.
func (g *Repo) Exclude() ([]byte, error) {
	b, err := g.dotgit.ReadFile(excludeFile)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, nil
	}
	if errors.Is(err, fs.ErrSymlink) || errors.Is(err, fs.ErrNotRegular) || errors.Is(err, fs.ErrNotDir) {
		return nil, fmt.Errorf("%w: %w", ErrExcludeSkipped, err)
	}
	if err != nil {
		return nil, fmt.Errorf("store/git: read %s: %w", excludeFile, err)
	}
	return b, nil
}

// EnsureExclude appends the reserved folders missing from .git/info/exclude, keeping every
// other line as it is. Init writes them for a repository it creates; an adopted one gets
// them here, on every open, so it is idempotent.
//
// The new file is written aside, made durable and renamed over the old one, so a crash
// leaves the old or the new file; the next open recomputes what is missing, so no intent is
// needed (ADR-0020), and it removes a leftover temp file first.
func (g *Repo) EnsureExclude() error {
	old, err := g.Exclude()
	if err != nil {
		return err
	}
	lines := map[string]bool{}
	for _, l := range bytes.Split(old, []byte("\n")) {
		lines[string(bytes.TrimSpace(l))] = true
	}
	next := old
	for _, r := range reservedExcludes {
		if lines[r] {
			continue
		}
		if len(next) > 0 && next[len(next)-1] != '\n' {
			next = append(next, '\n')
		}
		next = append(next, r+"\n"...)
	}
	if len(next) == len(old) {
		return nil
	}
	mode := g.excludeMode()
	if old == nil {
		if err := g.dotgit.Mkdir("info"); err != nil && !errors.Is(err, iofs.ErrExist) {
			return fmt.Errorf("store/git: create info: %w", err)
		}
		if err := g.dotgit.SyncDir("."); err != nil {
			return fmt.Errorf("store/git: create info: %w", err)
		}
	}
	if err := g.dotgit.Remove(excludeNew); err != nil && !errors.Is(err, iofs.ErrNotExist) {
		return fmt.Errorf("store/git: remove %s: %w", excludeNew, err)
	}
	steps := []func() error{
		func() error { return g.dotgit.CreateFile(excludeNew, next, mode) },
		func() error { return g.dotgit.SyncFile(excludeNew) },
		func() error { return g.dotgit.Rename(excludeNew, excludeFile) },
		func() error { return g.dotgit.SyncDir("info") },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return fmt.Errorf("store/git: update %s: %w", excludeFile, err)
		}
	}
	return nil
}

// excludeMode is info/exclude's permission bits, kept when it is rewritten (0644, git's
// default, for a new one).
func (g *Repo) excludeMode() uint32 {
	entries, err := g.dotgit.ReadDir("info")
	if err != nil {
		return 0o644
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && e.Name() == "exclude" {
			return uint32(info.Mode().Perm())
		}
	}
	return 0o644
}
