//go:build linux

package layout

import (
	"bytes"
	"errors"
	"fmt"
	iofs "io/fs"
	"strconv"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// FormatVersion is the format of the data this build writes in /data (ADR-0025). It goes up
// only when a later build changes what it stores in a way this one can't read.
const FormatVersion = 1

// Files in the state directory.
const (
	formatFile = "format"
	formatNew  = formatFile + ".new" // ours alone: a leftover is removed
)

var (
	// ErrNewerFormat: a newer PostelMD wrote this data; this build never reads or changes it.
	ErrNewerFormat = errors.New("layout: newer data format")
	// ErrBadFormat: the format file holds something other than a format number.
	ErrBadFormat = errors.New("layout: damaged data format file")
)

// EnsureFormat checks the data format in the state directory, under the instance lock, before
// the app state is read and before the vault is opened or swept (ADR-0025):
//   - missing: a new or a pre-format data directory; it writes FormatVersion;
//   - equal: nothing to do;
//   - newer: refused, so an older binary never touches data it doesn't understand;
//   - anything else: refused as damaged.
//
// The write is a synced temp file, one rename and the directory's fsync: one step that
// carries meaning, so a crash leaves no format file (the next start writes it) or a whole
// one, and no intent is needed (CLAUDE.md rule 14).
func EnsureFormat(state string, opts ...fs.Option) error {
	r, err := fs.OpenRoot(state, opts...)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	b, err := r.ReadFile(formatFile)
	switch {
	case err == nil:
		return checkFormat(state, b)
	case !errors.Is(err, iofs.ErrNotExist):
		return fmt.Errorf("layout: read %s/%s: %w", state, formatFile, err)
	}
	if err := r.Remove(formatNew); err != nil && !errors.Is(err, iofs.ErrNotExist) {
		return fmt.Errorf("layout: remove %s: %w", formatNew, err)
	}
	steps := []func() error{
		func() error { return r.CreateFile(formatNew, []byte(strconv.Itoa(FormatVersion)+"\n"), 0o600) },
		func() error { return r.Rename(formatNew, formatFile) },
		func() error { return r.SyncDir(".") },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return fmt.Errorf("layout: write %s/%s: %w", state, formatFile, err)
		}
	}
	return nil
}

func checkFormat(state string, b []byte) error {
	s := string(bytes.TrimSuffix(b, []byte("\n")))
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || s != strconv.Itoa(n) {
		return fmt.Errorf("%w: %s/%s is damaged: it must hold a data format number. Back up the data "+
			"directory first. If only PostelMD 0.1 ever used it, delete that file and start again (it is written "+
			"anew); otherwise restore the backup you took before the last upgrade (ADR-0025)", ErrBadFormat, state, formatFile)
	}
	if n > FormatVersion {
		return fmt.Errorf("%w: this data was written by a newer PostelMD (format %d); run that version or newer",
			ErrNewerFormat, n)
	}
	return nil
}
