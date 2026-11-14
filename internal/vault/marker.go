package vault

// The vault marker's content (ADR-0031): .postelmd-tmp/.created-by-postelmd holds
// "postelmd-vault 1", the vault UUID and the data format, one per line. v0.1 wrote it empty
// and reads only whether it exists (ADR-0027), so both read the same vault as theirs.

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"regexp"
	"strconv"
	"strings"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

const (
	markerTemp    = ownerMarker + ".new" // in TmpDir; the startup sweep removes a leftover
	markerHeader  = "postelmd-vault 1"
	markerMaxSize = 256 // ours is under 64 bytes
)

// ErrBadMarker: the marker is there but isn't ADR-0031's three lines, or isn't a regular
// file (a symlink, a folder), or the temp folder isn't a folder. Startup refuses it and
// changes nothing; postelmd vault claim rewrites it (ADR-0030's format table).
var ErrBadMarker = errors.New("vault: the vault marker (.postelmd-tmp/.created-by-postelmd) " +
	"can't be read as PostelMD's")

var (
	markerUUID   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	markerFormat = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
)

// Marker is what ReadMarker found. The zero Marker is "absent"; Present with no UUID is the
// empty marker v0.1 writes.
type Marker struct {
	Present   bool
	VaultUUID string
	Format    int
}

// ReadMarker reads the marker of the vault at dir without opening the vault: Open adopts
// the folder, which would create an empty marker where the format table must see none
// (ADR-0030). It changes nothing. The caller holds the instance lock (CLAUDE.md rule 12).
func ReadMarker(dir string, opts ...fs.Option) (Marker, error) {
	root, err := fs.OpenRoot(dir, opts...)
	if err != nil {
		return Marker{}, err
	}
	defer func() { _ = root.Close() }()
	rel := TmpDir + "/" + ownerMarker
	st, err := root.Stat(rel)
	switch {
	case errors.Is(err, iofs.ErrNotExist):
		return Marker{}, nil
	case errors.Is(err, fs.ErrSymlink), errors.Is(err, fs.ErrNotRegular), errors.Is(err, fs.ErrNotDir):
		return Marker{}, fmt.Errorf("%w: it isn't a regular file", ErrBadMarker)
	case err != nil:
		return Marker{}, fmt.Errorf("vault: read the vault marker: %w", err)
	case st.Size > markerMaxSize:
		return Marker{}, fmt.Errorf("%w: it is too large", ErrBadMarker)
	}
	b, err := root.ReadFile(rel)
	if errors.Is(err, fs.ErrSymlink) || errors.Is(err, fs.ErrNotRegular) {
		return Marker{}, fmt.Errorf("%w: it isn't a regular file", ErrBadMarker)
	} else if err != nil {
		return Marker{}, fmt.Errorf("vault: read the vault marker: %w", err)
	}
	return parseMarker(b)
}

func parseMarker(b []byte) (Marker, error) {
	if len(b) == 0 {
		return Marker{Present: true}, nil
	}
	if len(b) > markerMaxSize {
		return Marker{}, fmt.Errorf("%w: it is too large", ErrBadMarker)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 3 || lines[0] != markerHeader || !markerUUID.MatchString(lines[1]) || !markerFormat.MatchString(lines[2]) {
		return Marker{}, fmt.Errorf("%w: its content isn't a vault UUID and format", ErrBadMarker)
	}
	format, _ := strconv.Atoi(lines[2])
	return Marker{Present: true, VaultUUID: lines[1], Format: format}, nil
}

// WriteMarker writes the marker of the vault at dir with vaultUUID and format: a temp in the
// temp folder, synced, renamed over the marker, and the folder synced (ADR-0031). It doesn't
// open the vault. The caller holds the instance lock (CLAUDE.md rule 12).
//
// Where there is no marker, it first adopts the folder as ADR-0027 does, refusal included:
// reserved folders holding anything are ErrForeignReserved, and nothing changes. Adopting
// creates the marker empty and makes it durable before the temp exists, so a crash before
// the rename leaves an adopted temp folder whose sweep removes the temp, never one that
// adoption would refuse as the user's. A marker that is a symlink (dangling or not), a
// folder or another special file is refused (ErrBadMarker) and left as it is.
//
// A crash leaves the marker as it was (or empty where there was none) or as written: the
// rename is the one step that carries meaning, so no intent is needed (CLAUDE.md rule 14;
// ADR-0031's table).
func WriteMarker(dir, vaultUUID string, format int, opts ...fs.Option) error {
	content := markerHeader + "\n" + vaultUUID + "\n" + strconv.Itoa(format) + "\n"
	if _, err := parseMarker([]byte(content)); err != nil {
		return fmt.Errorf("vault: not a vault marker's UUID and format: %w", err)
	}
	root, err := fs.OpenRoot(dir, opts...)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	rel, tmp := TmpDir+"/"+ownerMarker, TmpDir+"/"+markerTemp
	_, err = root.Stat(rel)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrSymlink), errors.Is(err, fs.ErrNotRegular):
		// A symlink is refused like a folder: replacing it would mark a folder that adoption
		// never checked, and the next sweep would delete what the user keeps beside it.
		return fmt.Errorf("%w: it (or its folder, %s) is a symlink, a folder or a special file; "+
			"move it aside, then run this again", ErrBadMarker, TmpDir)
	case errors.Is(err, iofs.ErrNotExist):
		if err := adoptForMarker(&Vault{root: root}); err != nil {
			return err
		}
	default:
		return err
	}
	if err := root.Remove(tmp); err != nil && !errors.Is(err, iofs.ErrNotExist) {
		return err
	}
	if err := root.CreateFile(tmp, []byte(content), newFilePerm); err != nil {
		return err
	}
	if err := root.Rename(tmp, rel); err != nil {
		return err
	}
	return root.SyncDir(TmpDir)
}

// adoptForMarker is adopt for a folder without the marker, before WriteMarker's temp: the
// same check, then the empty marker, durable.
func adoptForMarker(v *Vault) error {
	if err := v.checkUnowned(); err != nil {
		return err
	}
	if err := v.ensureDir(TmpDir); err != nil {
		return err
	}
	rel := TmpDir + "/" + ownerMarker
	if err := v.root.CreateFile(rel, nil, newFilePerm); err != nil {
		return err
	}
	// This sync orders the marker's entry before the temp's. faultfs can't show why it is
	// needed: it undoes every unsynced entry of a folder together, so without it the two are
	// lost or kept as one. A filesystem that keeps the later entry (the temp) and loses the
	// earlier one (the marker) would leave a temp folder that Open's adoption refuses.
	return v.root.SyncDir(TmpDir)
}
