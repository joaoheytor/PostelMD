package vault

import (
	"errors"
	"fmt"
	iofs "io/fs"
)

// ownerMarker is the empty file PostelMD keeps in the temp folder once it owns the reserved
// folders (ADR-0020, Clarifications): from then on, everything in .postelmd-tmp and
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
			return fmt.Errorf("%w: %s in your notes folder isn't empty, and PostelMD uses that folder for "+
				"itself (it empties %s at every start). Rename %s, for example to %s-mine, and start again. "+
				"If PostelMD made it, with a build from before this check, create the empty file %s/%s instead (ADR-0020)",
				ErrForeignReserved, d, TmpDir, d, d[1:], TmpDir, ownerMarker)
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
