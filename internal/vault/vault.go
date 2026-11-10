// Package vault is the MVP's note store (ADR-0020): reads, conditional saves, creates,
// moves and trash deletes on one vault, serialized by one mutex. Every mutation is a single
// atomic rename, so none needs an intent (CLAUDE.md rule 14). All file I/O goes through
// internal/store/fs, and every path a caller supplies is checked with fs.IsReserved.
package vault

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	iofs "io/fs"
	"path"
	"time"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// Reserved directories at the vault root (ADR-0006).
const (
	TmpDir   = ".postelmd-tmp"   // temp files of in-flight saves
	TrashDir = ".postelmd-trash" // deleted files and folders
)

// MaxNoteSize is the largest note a save or create accepts (ADR-0003 §8).
const MaxNoteSize = 5 << 20

var (
	ErrNotFound = errors.New("vault: not found") // also returned for reserved paths
	ErrExists   = errors.New("vault: already exists")
	ErrConflict = errors.New("vault: the note changed since it was read")
	ErrBusy     = errors.New("vault: busy, retry")
	ErrTooLarge = errors.New("vault: note too large")
)

// ConflictError is a failed save precondition. It carries the current note for the
// conflict view.
type ConflictError struct {
	Current []byte
	Rev     Rev
}

func (e *ConflictError) Error() string { return ErrConflict.Error() }
func (e *ConflictError) Unwrap() error { return ErrConflict }

// Rev identifies a note's content: the hex SHA-256 of its bytes (the ETag; ADR-0020).
type Rev string

// RevOf returns the revision of content.
func RevOf(content []byte) Rev {
	sum := sha256.Sum256(content)
	return Rev(hex.EncodeToString(sum[:]))
}

// Vault is one open vault.
type Vault struct {
	root *fs.Root
	mu   chan struct{} // the vault mutex; a channel so waiting is bounded
	wait time.Duration
	now  func() time.Time
}

// Open opens the vault at dir and removes every leftover temp file: a save that didn't
// finish never answered 200, so its edit is still the client's draft. Call it before
// serving requests.
func Open(dir string, opts ...fs.Option) (*Vault, error) {
	root, err := fs.OpenRoot(dir, opts...)
	if err != nil {
		return nil, err
	}
	v := &Vault{root: root, mu: make(chan struct{}, 1), wait: 5 * time.Second, now: time.Now}
	if err := v.sweepTemps(); err != nil {
		_ = root.Close()
		return nil, err
	}
	return v, nil
}

// Close releases the vault's directory handle.
func (v *Vault) Close() error { return v.root.Close() }

func (v *Vault) sweepTemps() error {
	if err := v.ensureDir(TmpDir); err != nil {
		return err
	}
	entries, err := v.root.ReadDir(TmpDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := v.root.Remove(TmpDir + "/" + e.Name()); err != nil {
			return fmt.Errorf("vault: sweep %s: %w", e.Name(), err)
		}
	}
	if len(entries) > 0 {
		return v.root.SyncDir(TmpDir)
	}
	return nil
}

// ensureDir creates a reserved directory at the root, durably, if it's missing.
func (v *Vault) ensureDir(name string) error {
	err := v.root.Mkdir(name)
	if errors.Is(err, iofs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return v.root.SyncDir(".")
}

// lock takes the vault mutex, waiting at most v.wait.
func (v *Vault) lock(ctx context.Context) (unlock func(), err error) {
	t := time.NewTimer(v.wait)
	defer t.Stop()
	select {
	case v.mu <- struct{}{}:
		return func() { <-v.mu }, nil
	case <-t.C:
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// checkPath maps reserved and malformed paths to ErrNotFound (ADR-0006: 404, not 403).
func checkPath(p string) error {
	if fs.IsReserved(p) {
		return fmt.Errorf("%w: %q", ErrNotFound, p)
	}
	return nil
}

// notFound maps every "there's nothing usable at that path" error from the store to
// ErrNotFound: missing, the wrong kind, or reached through a symlink (never followed).
func notFound(err error) error {
	for _, e := range []error{iofs.ErrNotExist, fs.ErrNotRegular, fs.ErrNotDir, fs.ErrSymlink, fs.ErrEscape, fs.ErrInvalidPath} {
		if errors.Is(err, e) {
			return fmt.Errorf("%w: %w", ErrNotFound, err)
		}
	}
	return err
}

// Kind of a folder entry.
type Kind string

const (
	KindFile   Kind = "file"
	KindFolder Kind = "folder"
	KindOther  Kind = "other" // a symlink, device or socket: listed, never opened
)

// Entry is one item in a folder listing.
type Entry struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
}

// List returns a folder's entries sorted by name ("" for the vault root). Reserved entries
// (.git, the trash, Syncthing files…) are hidden.
func (v *Vault) List(p string) ([]Entry, error) {
	dir := "."
	if p != "" {
		if err := checkPath(p); err != nil {
			return nil, err
		}
		dir = p
	}
	des, err := v.root.ReadDir(dir)
	if err != nil {
		return nil, notFound(err)
	}
	out := make([]Entry, 0, len(des))
	for _, d := range des {
		if fs.IsReserved(path.Join(p, d.Name())) {
			continue
		}
		kind := KindOther
		switch {
		case d.Type().IsRegular():
			kind = KindFile
		case d.IsDir():
			kind = KindFolder
		}
		out = append(out, Entry{Name: d.Name(), Kind: kind})
	}
	return out, nil
}

// Read returns a note and its revision. It takes no lock: a note is only ever replaced by
// an atomic rename, so a read sees one whole version.
func (v *Vault) Read(p string) ([]byte, Rev, error) {
	if err := checkPath(p); err != nil {
		return nil, "", err
	}
	b, err := v.root.ReadFile(p)
	if err != nil {
		return nil, "", notFound(err)
	}
	return b, RevOf(b), nil
}

// writeTemp writes content to a new, synced temp file and returns its path.
func (v *Vault) writeTemp(content []byte) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	tmp := TmpDir + "/" + hex.EncodeToString(id[:])
	return tmp, v.root.CreateFile(tmp, content, 0o600)
}

// syncDirs fsyncs each directory once, in order.
func (v *Vault) syncDirs(dirs ...string) error {
	seen := map[string]bool{}
	for _, d := range dirs {
		if seen[d] {
			continue
		}
		seen[d] = true
		if err := v.root.SyncDir(d); err != nil {
			return err
		}
	}
	return nil
}

func parent(p string) string { return path.Dir(p) }
