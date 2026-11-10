//go:build !linux

package layout

import (
	"errors"
	"fmt"
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
type Options struct{ AllowNetworkFS, AllowEphemeralData bool }

// ErrNotesAtRoot: notes were found directly in the data directory.
var ErrNotesAtRoot = errors.New("layout: Markdown files found directly in the data directory")

// ErrNoVolume: no volume is mounted at /data.
var ErrNoVolume = errors.New("layout: no volume is mounted at /data")

// Prepare is unsupported outside Linux (issue #34).
func Prepare(dataDir string, _ Options) (Paths, Durability, error) {
	return Paths{}, 0, fmt.Errorf("layout: %s: %w", dataDir, errors.ErrUnsupported)
}
