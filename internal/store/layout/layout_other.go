//go:build !linux

package layout

import (
	"errors"
	"fmt"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// Paths under the data directory.
type Paths struct{ State, Vault, Lock string }

// Durability of the data directory.
type Durability int

const (
	Persistent Durability = iota
	Ephemeral
	ContainerLayer
)

// Options are the operator's overrides.
type Options struct{ AllowNetworkFS, AllowEphemeralData, OpensDatabase bool }

// ErrNotesAtRoot: notes were found directly in the data directory.
var ErrNotesAtRoot = errors.New("layout: Markdown files found directly in the data directory")

// ErrNoVolume: no volume is mounted at /data.
var ErrNoVolume = errors.New("layout: no volume is mounted at /data")

// FormatVersion is the format of the data this build writes in /data (ADR-0025).
const FormatVersion = 1

// EnsureFormat is unsupported outside Linux (issue #34).
func EnsureFormat(state string, _ ...fs.Option) error {
	return fmt.Errorf("layout: %s: %w", state, errors.ErrUnsupported)
}

// Prepare is unsupported outside Linux (issue #34).
func Prepare(dataDir string, _ Options) (Paths, Durability, error) {
	return Paths{}, 0, fmt.Errorf("layout: %s: %w", dataDir, errors.ErrUnsupported)
}
