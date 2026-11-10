//go:build !linux

package layout

import (
	"errors"
	"fmt"
)

// Paths under the data directory.
type Paths struct{ State, Vault, Lock string }

// Durability of the state filesystem.
type Durability int

const (
	Persistent Durability = iota
	Ephemeral
)

// ErrNotesAtRoot: notes were found directly in the data directory.
var ErrNotesAtRoot = errors.New("layout: Markdown files found directly in the data directory")

// Prepare is unsupported outside Linux (issue #34).
func Prepare(dataDir string, _ bool) (Paths, Durability, error) {
	return Paths{}, 0, fmt.Errorf("layout: %s: %w", dataDir, errors.ErrUnsupported)
}
