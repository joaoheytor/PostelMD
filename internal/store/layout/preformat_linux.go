//go:build linux

package layout

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"strconv"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// PreFormat2 is the first upgrade's copy of the v0.1 state (ADR-0030's step 1, mechanics in
// ADR-0041): what a rollback to v0.1 restores.
const PreFormat2 = "pre-format-2"

const preFormat2Complete = "complete"

// preFormat2Files are copied when present, in this order.
var preFormat2Files = []string{formatFile, "auth.json", "secret.key", "config.yaml"}

// CopyPreFormat2 is the first upgrade's step 1. A pre-format-2/ whose complete file is a
// regular file holding tag, the upgrade's own (its intent's vault UUID and time), is kept
// (kept is true: this upgrade, resumed). Any other is first moved aside, with
// RENAME_NOREPLACE, to pre-format-2-<stamp>/ (or -<stamp>-<n>), never written over or kept:
// an earlier upgrade's copy may hold credentials since revoked. Then each file present is
// copied as <name>.tmp (0600, O_EXCL, fsync) renamed to <name>, the folder is synced, and its
// complete file is created with tag and synced, and the folder synced again. Every source is read before
// anything is written, so a source that isn't a regular file refuses with nothing changed.
// step, if not nil, is called with "moved-aside", "copied" (after each file) and "complete":
// the crash test's kill points. The caller holds the instance lock.
func CopyPreFormat2(state, stamp, tag string, step func(string), opts ...fs.Option) (kept bool, err error) {
	if step == nil {
		step = func(string) {}
	}
	r, err := fs.OpenRoot(state, opts...)
	if err != nil {
		return false, err
	}
	defer func() { _ = r.Close() }()
	exists, complete, err := preFormat2State(r, tag)
	if err != nil || complete {
		return complete, err
	}
	srcs, err := readSources(r)
	if err != nil {
		return false, err
	}
	if exists {
		if err := moveAside(r, stamp); err != nil {
			return false, err
		}
		step("moved-aside")
	}
	return false, writeCopy(r, srcs, tag, step)
}

// preFormat2State reports whether pre-format-2/ is there, and whether this upgrade completed
// it: its complete file is a regular file holding tag. A symlink or a file at the folder's
// name is refused.
func preFormat2State(r *fs.Root, tag string) (exists, complete bool, err error) {
	_, err = r.ReadDir(PreFormat2)
	switch {
	case errors.Is(err, iofs.ErrNotExist):
		return false, false, nil
	case err != nil:
		return false, false, fmt.Errorf("layout: %s isn't a folder PostelMD made; move it aside: %w", PreFormat2, err)
	}
	b, err := r.ReadFile(PreFormat2 + "/" + preFormat2Complete)
	switch {
	case errors.Is(err, iofs.ErrNotExist), errors.Is(err, fs.ErrSymlink), errors.Is(err, fs.ErrNotRegular):
		return true, false, nil
	case err != nil:
		return true, false, fmt.Errorf("layout: read %s/%s: %w", PreFormat2, preFormat2Complete, err)
	}
	return true, string(b) == tag, nil
}

type source struct {
	name string
	data []byte
}

func readSources(r *fs.Root) ([]source, error) {
	var out []source
	for _, n := range preFormat2Files {
		b, err := r.ReadFile(n)
		switch {
		case errors.Is(err, iofs.ErrNotExist):
			continue
		case err != nil:
			return nil, fmt.Errorf("layout: copy %s into %s: %w", n, PreFormat2, err)
		}
		out = append(out, source{n, b})
	}
	return out, nil
}

// moveAside renames the incomplete pre-format-2/ to the first free name and syncs the state
// directory.
func moveAside(r *fs.Root, stamp string) error {
	base := PreFormat2 + "-" + stamp
	for n := 1; ; n++ {
		to := base
		if n > 1 {
			to = base + "-" + strconv.Itoa(n)
		}
		err := r.RenameNoReplace(PreFormat2, to)
		if errors.Is(err, iofs.ErrExist) && n < 1000 {
			continue
		}
		if err != nil {
			return fmt.Errorf("layout: move the incomplete %s aside: %w", PreFormat2, err)
		}
		return r.SyncDir(".")
	}
}

func writeCopy(r *fs.Root, srcs []source, tag string, step func(string)) error {
	if err := r.Mkdir(PreFormat2); err != nil {
		return fmt.Errorf("layout: create %s: %w", PreFormat2, err)
	}
	if err := r.SyncDir("."); err != nil {
		return err
	}
	for _, s := range srcs {
		tmp := PreFormat2 + "/" + s.name + ".tmp"
		if err := r.CreateFile(tmp, s.data, 0o600); err != nil {
			return fmt.Errorf("layout: copy %s into %s: %w", s.name, PreFormat2, err)
		}
		if err := r.Rename(tmp, PreFormat2+"/"+s.name); err != nil {
			return fmt.Errorf("layout: copy %s into %s: %w", s.name, PreFormat2, err)
		}
		step("copied")
	}
	if err := r.SyncDir(PreFormat2); err != nil {
		return err
	}
	if err := r.CreateFile(PreFormat2+"/"+preFormat2Complete, []byte(tag), 0o600); err != nil {
		return fmt.Errorf("layout: complete %s: %w", PreFormat2, err)
	}
	if err := r.SyncDir(PreFormat2); err != nil {
		return err
	}
	step("complete")
	return nil
}
