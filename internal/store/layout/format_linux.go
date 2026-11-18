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
// only when a later build changes what it stores in a way this one can't read. Format 2 is
// file IDs (ADR-0030); 0.1 writes 1 and can't open 2.
const FormatVersion = 2

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

// CheckFormat checks the data format in the state directory for a build that writes data
// format build (FormatVersion), under the instance lock, before the app state is read and
// before the vault is opened or swept (ADR-0025), and returns the format the data is at:
//   - missing: a new or a pre-format data directory. It is format 1 and nothing is written:
//     ADR-0030's format table runs the first upgrade, whose last step writes the format
//     (ADR-0041's follow-up, ADR-0042);
//   - at most build: nothing to do;
//   - newer: refused, so an older binary never touches data it doesn't understand;
//   - anything else: refused as damaged.
//
// It never writes: SetFormat does, at the end of the first upgrade and of db rebuild.
func CheckFormat(state string, build int, opts ...fs.Option) (int, error) {
	r, err := fs.OpenRoot(state, opts...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = r.Close() }()
	b, err := r.ReadFile(formatFile)
	switch {
	case err == nil:
		return checkFormat(state, b, build)
	case !errors.Is(err, iofs.ErrNotExist):
		return 0, fmt.Errorf("layout: read %s/%s: %w", state, formatFile, err)
	}
	return 1, nil
}

// SetFormat writes n into the format file, replacing what is there: the last step of the
// first upgrade and of db rebuild (ADR-0030). The write is a synced temp file, one rename
// and the directory's fsync: one step that carries meaning, so a crash leaves the old format
// or n, never a torn one, and no intent is needed (CLAUDE.md rule 14). The caller holds the
// instance lock.
func SetFormat(state string, n int, opts ...fs.Option) error {
	if n < 1 || n > FormatVersion {
		return fmt.Errorf("layout: data format %d isn't one this build writes", n)
	}
	r, err := fs.OpenRoot(state, opts...)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return writeFormat(r, state, n)
}

// writeFormat writes n through a synced temp, one rename and the folder's fsync.
func writeFormat(r *fs.Root, state string, n int) error {
	if err := r.Remove(formatNew); err != nil && !errors.Is(err, iofs.ErrNotExist) {
		return fmt.Errorf("layout: remove %s: %w", formatNew, err)
	}
	steps := []func() error{
		func() error { return r.CreateFile(formatNew, []byte(strconv.Itoa(n)+"\n"), 0o600) },
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

func checkFormat(state string, b []byte, build int) (int, error) {
	s := string(bytes.TrimSuffix(b, []byte("\n")))
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || s != strconv.Itoa(n) {
		return 0, fmt.Errorf("%w: %s/%s is damaged: it must hold a data format number. Back up the data "+
			"directory first. If only PostelMD 0.1 ever used it, delete that file and start again (it is written "+
			"anew); otherwise restore the backup you took before the last upgrade (ADR-0025)", ErrBadFormat, state, formatFile)
	}
	if n > build {
		return 0, fmt.Errorf("%w: this data was written by a newer PostelMD (format %d); run that version or newer",
			ErrNewerFormat, n)
	}
	return n, nil
}
