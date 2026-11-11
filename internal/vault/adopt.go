package vault

// What Open does before serving: adopt the reserved folders, sweep the temp folder, probe
// for RENAME_NOREPLACE.

import (
	"errors"
	"fmt"
	iofs "io/fs"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// ownerMarker is the empty file PostelMD keeps in the temp folder once it owns the reserved
// folders (ADR-0027): from then on, everything in .postelmd-tmp and
// .postelmd-trash is PostelMD's own. The sweep never removes it.
const ownerMarker = ".created-by-postelmd"

// ErrForeignReserved: an adopted folder already has a .postelmd-tmp or .postelmd-trash with
// entries PostelMD didn't create. Open refuses it and changes nothing: the startup sweep
// would delete whatever is in the temp folder.
var ErrForeignReserved = errors.New("vault: a reserved folder holds files PostelMD didn't create")

// adopt runs before anything else touches the reserved folders. With the marker in place,
// they are PostelMD's. Without it, this is the vault's first start (or an older build's
// vault): each reserved folder must be missing or empty, and then the marker is written,
// durably, before the sweep or the rename probe can put anything there.
//
// One side effect that carries meaning (the marker's create), so no intent: a crash before
// its directory sync leaves an empty temp folder at most, which the next start adopts again.
// The temp folder is synced before Open returns, so the marker is durable before any delete
// can fill the trash.
func (v *Vault) adopt() error {
	_, err := v.root.ReadFile(TmpDir + "/" + ownerMarker)
	if err == nil {
		return nil
	}
	if !errors.Is(err, iofs.ErrNotExist) {
		return fmt.Errorf("vault: check %s: %w", TmpDir, err)
	}
	for _, d := range []string{TmpDir, TrashDir} {
		entries, err := v.root.ReadDir(d)
		if errors.Is(err, iofs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("vault: check %s: %w", d, err)
		}
		if len(entries) > 0 {
			return foreignReserved(d)
		}
	}
	if err := v.ensureDir(TmpDir); err != nil {
		return err
	}
	if err := v.root.CreateFile(TmpDir+"/"+ownerMarker, nil, newFilePerm); err != nil && !errors.Is(err, iofs.ErrExist) {
		return err
	}
	return v.root.SyncDir(TmpDir)
}

// sweepTemps removes every leftover temp file, apart from the marker: a save that didn't
// finish never answered 200, so its edit is still the client's draft.
func (v *Vault) sweepTemps() error {
	if err := v.ensureDir(TmpDir); err != nil {
		return err
	}
	entries, err := v.root.ReadDir(TmpDir)
	if err != nil {
		return err
	}
	swept := 0
	for _, e := range entries {
		if e.Name() == ownerMarker {
			continue
		}
		swept++
		rm := v.root.Remove
		if e.IsDir() { // a repository Init didn't finish
			rm = v.removeTree
		}
		if err := rm(TmpDir + "/" + e.Name()); err != nil {
			return fmt.Errorf("vault: sweep %s: %w", e.Name(), err)
		}
	}
	if swept > 0 {
		return v.root.SyncDir(TmpDir)
	}
	return nil
}

// probeNoReplace renames a temp file to an absent name with RENAME_NOREPLACE. The target
// must be absent: the VFS answers EEXIST for an existing one before asking the filesystem,
// which would prove nothing (ADR-0014 §1).
func (v *Vault) probeNoReplace() error {
	tmp, err := v.writeTemp(nil, newFilePerm)
	if err != nil {
		return err
	}
	probe := tmp + ".probe"
	err = v.root.RenameNoReplace(tmp, probe)
	if errors.Is(err, fs.ErrNoReplaceUnsupported) {
		return errors.Join(fmt.Errorf("vault: this filesystem can't rename without replacing, "+
			"so moves could overwrite notes; use a named volume or a WSL-side folder (ADR-0020): %w", err), v.root.Remove(tmp))
	}
	if err != nil {
		return errors.Join(err, v.root.Remove(tmp))
	}
	return v.root.Remove(probe)
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

// foreignReserved is the refusal for reserved folder d (ADR-0027). The usual cause is
// PostelMD's own folders without the marker (a build from before it, a hand-deleted temp
// folder, git clean -X), so the marker fix comes first; renaming is for a folder that really
// is the user's, and renaming PostelMD's trash would turn deleted notes into ordinary ones.
func foreignReserved(d string) error {
	fix := "mkdir -p " + TmpDir + " && touch " + TmpDir + "/" + ownerMarker
	rename := "PostelMD empties " + TmpDir + " at every start"
	if d == TrashDir {
		rename = "renaming PostelMD's own trash would make the deleted notes in it visible and committable again"
	}
	return fmt.Errorf("%w: %s in your notes folder isn't empty, and PostelMD didn't mark it as its own. "+
		"If PostelMD made it (a build before 0.1.0-rc.1, or the marker was deleted), run this in the notes folder "+
		"and start again: %s. If the folder is yours, rename %s instead (%s). Nothing was changed (ADR-0027)",
		ErrForeignReserved, d, fix, d, rename)
}
