// Package vault is the MVP's note store (ADR-0020): reads, conditional saves, creates,
// moves and trash deletes on one vault, serialized by one mutex. Every mutation's one data-bearing
// step is a single atomic rename, so none needs an intent (CLAUDE.md rule 14). A delete first makes the
// empty trash folder it renames into: that step holds no data, so a crash after it needs no
// recovery (ADR-0022's table); a restore from the trash removes that folder after its rename,
// and a crash between them leaves the same empty folder (ADR-0024). All file I/O goes through
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
	"log/slog"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/git"
)

// Reserved directories at the vault root (ADR-0006).
const (
	TmpDir   = ".postelmd-tmp"   // temp files of in-flight saves
	TrashDir = ".postelmd-trash" // deleted files and folders
)

// MaxNoteSize is the largest note a save or create accepts (ADR-0003 §8).
const MaxNoteSize = 5 << 20

var (
	ErrNotFound   = errors.New("vault: not found") // also returned for reserved paths
	ErrExists     = errors.New("vault: already exists")
	ErrConflict   = errors.New("vault: the note changed since it was read")
	ErrBusy       = errors.New("vault: busy, retry")
	ErrTooLarge   = errors.New("vault: note too large")
	ErrIntoItself = errors.New("vault: a folder can't move into itself")
	// ErrNameTooLong: a new name has a part over MaxNameBytes, the filesystem's limit.
	ErrNameTooLong = errors.New("vault: a file or folder name can be at most 255 bytes")
	// ErrTooLargeToRead: Read refused a file over the store's read limit (64 MiB).
	ErrTooLargeToRead = errors.New("vault: file too large to read")
)

// MaxNameBytes is the longest file or folder name the filesystem takes (Linux NAME_MAX).
const MaxNameBytes = 255

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
	dir  string
	opts []fs.Option
	git  *git.Repo     // nil until EnableGit
	mu   chan struct{} // the vault mutex; a channel so waiting is bounded
	wait time.Duration
	now  func() time.Time
	// afterCheck, in tests only, runs once a commit's check has passed, still under the mutex.
	afterCheck func()
	// onWait, in tests only, runs when lock finds the mutex held, just before it waits.
	onWait func()
	log    *slog.Logger // warnings only; never note content (CLAUDE.md rule 15)
}

// Open opens the vault at dir, refuses an adopted folder whose reserved folders hold entries
// PostelMD didn't create (adopt), removes every leftover temp file (a save that didn't finish
// never answered 200, so its edit is still the client's draft), and refuses a filesystem
// without RENAME_NOREPLACE (ADR-0020). Call it before serving requests.
func Open(dir string, opts ...fs.Option) (*Vault, error) {
	root, err := fs.OpenRoot(dir, opts...)
	if err != nil {
		return nil, err
	}
	v := &Vault{root: root, dir: dir, opts: opts, mu: make(chan struct{}, 1), wait: 5 * time.Second, now: time.Now,
		log: slog.New(slog.DiscardHandler)}
	err = v.adopt()
	if err == nil {
		err = v.sweepTemps()
	}
	if err == nil {
		err = v.probeNoReplace()
	}
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return v, nil
}

// SetLogger sets where the vault's warnings go (discarded by default). Call it before
// EnableGit.
func (v *Vault) SetLogger(l *slog.Logger) { v.log = l }

// Close releases the vault's directory handles.
func (v *Vault) Close() error {
	var err error
	if v.git != nil {
		err = v.git.Close()
	}
	return errors.Join(err, v.root.Close())
}

// lock takes the vault mutex, waiting at most v.wait.
func (v *Vault) lock(ctx context.Context) (unlock func(), err error) {
	select {
	case v.mu <- struct{}{}:
		return func() { <-v.mu }, nil
	default:
	}
	if v.onWait != nil {
		v.onWait()
	}
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

// checkNewPath is checkPath for a path a create, mkdir or move is about to make: a part
// longer than the filesystem takes is ErrNameTooLong, the user's to fix, not an internal error.
func checkNewPath(p string) error {
	if err := checkPath(p); err != nil {
		return err
	}
	for _, part := range strings.Split(p, "/") {
		if len(part) > MaxNameBytes {
			return fmt.Errorf("%w: %d bytes", ErrNameTooLong, len(part))
		}
	}
	return nil
}

// notFound maps every "there's nothing usable at that path" error from the store to
// ErrNotFound: missing, the wrong kind, reached through a symlink (never followed), or a
// name too long to exist on disk.
func notFound(err error) error {
	for _, e := range []error{iofs.ErrNotExist, fs.ErrNotRegular, fs.ErrNotDir, fs.ErrSymlink, fs.ErrEscape, fs.ErrInvalidPath, syscall.ENAMETOOLONG} {
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

// errEnough stops a walk once Files has max paths.
var errEnough = errors.New("enough files")

// Files returns every file's path in the vault, sorted, for the finder: at most max, and
// truncated is true when there were more. Reserved paths and symlinks are never listed (walk).
func (v *Vault) Files(max int) (files []string, truncated bool, err error) {
	err = v.walk(".", func(p string, _ int64) error {
		if len(files) == max {
			truncated = true
			return errEnough
		}
		files = append(files, p)
		return nil
	})
	if err != nil && !errors.Is(err, errEnough) {
		return nil, false, err
	}
	sort.Strings(files)
	return files, truncated, nil
}

// Read returns a note and its revision. It takes no lock: a note is only ever replaced by
// an atomic rename, so a read sees one whole version.
func (v *Vault) Read(p string) ([]byte, Rev, error) {
	if err := checkPath(p); err != nil {
		return nil, "", err
	}
	b, err := v.root.ReadFile(p)
	if errors.Is(err, fs.ErrTooLarge) {
		return nil, "", fmt.Errorf("%w: %w", ErrTooLargeToRead, err)
	}
	if err != nil {
		return nil, "", notFound(err)
	}
	return b, RevOf(b), nil
}

// newFilePerm is the mode of a note PostelMD creates: private, like the rest of /data.
const newFilePerm = 0o600

// writeTemp writes content to a new, synced temp file with exactly perm's permission bits
// and returns its path.
func (v *Vault) writeTemp(content []byte, perm uint32) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	tmp := TmpDir + "/" + hex.EncodeToString(id[:])
	return tmp, v.root.CreateFileMode(tmp, content, perm)
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
