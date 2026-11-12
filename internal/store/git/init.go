package git

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	iofs "io/fs"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// DefaultBranch is the branch a new repository starts on.
const DefaultBranch = "main"

// ErrGitFile: the vault's .git is a file (a linked worktree or a submodule), which the MVP
// doesn't support.
var ErrGitFile = errors.New("store/git: .git is a file (a linked worktree or submodule), not a repository")

// Init creates an empty repository at vault/.git if there is none, and reports whether it
// did. The repository is built in tmpDir (a vault temp directory the caller sweeps at
// startup), made durable, and renamed into place, so a crash leaves either no .git or a
// complete one.
func Init(vault *fs.Root, tmpDir string) (bool, error) {
	if _, err := vault.ReadDir(".git"); err == nil {
		return false, nil
	} else if errors.Is(err, fs.ErrNotDir) {
		return false, ErrGitFile
	} else if !errors.Is(err, iofs.ErrNotExist) {
		return false, fmt.Errorf("store/git: check .git: %w", err)
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return false, err
	}
	tmp := tmpDir + "/git-" + hex.EncodeToString(id[:])
	dirs := []string{"", "/info", "/objects", "/objects/info", "/objects/pack", "/refs", "/refs/heads", "/refs/tags"}
	for _, d := range dirs {
		if err := vault.Mkdir(tmp + d); err != nil {
			return false, fmt.Errorf("store/git: init: %w", err)
		}
	}
	fold, err := foldsCase(vault, tmp)
	if err != nil {
		return false, fmt.Errorf("store/git: init: probe case folding: %w", err)
	}
	config := "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n"
	if fold {
		config += "\tignorecase = true\n"
	}
	files := map[string]string{
		"/HEAD":   "ref: refs/heads/" + DefaultBranch + "\n",
		"/config": config,
		// The user's own git shouldn't list PostelMD's reserved folders as untracked.
		"/info/exclude": "/.postelmd-trash/\n/.postelmd-tmp/\n",
	}
	for name, content := range files {
		if err := vault.CreateFile(tmp+name, []byte(content), 0o644); err != nil {
			return false, fmt.Errorf("store/git: init: %w", err)
		}
		if err := vault.SyncFile(tmp + name); err != nil {
			return false, fmt.Errorf("store/git: init: %w", err)
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := vault.SyncDir(tmp + dirs[i]); err != nil {
			return false, fmt.Errorf("store/git: init: %w", err)
		}
	}
	if err := vault.RenameNoReplace(tmp, ".git"); err != nil {
		return false, fmt.Errorf("store/git: init: %w", err)
	}
	for _, d := range []string{".", tmpDir} { // destination first
		if err := vault.SyncDir(d); err != nil {
			return false, fmt.Errorf("store/git: init: %w", err)
		}
	}
	return true, nil
}

// foldsCase is probeCaseFold, replaced in tests: the test machine's filesystem doesn't fold.
var foldsCase = probeCaseFold

// probeCaseFold reports whether the filesystem under dir folds case, as git init finds out
// for core.ignorecase: it creates a file and looks it up in another letter case, then
// removes it. dir is the repository being built, inside the vault's temp directory.
func probeCaseFold(vault *fs.Root, dir string) (bool, error) {
	probe := dir + "/case-probe"
	if err := vault.CreateFile(probe, nil, 0o600); err != nil {
		return false, err
	}
	_, err := vault.Perm(dir + "/CASE-PROBE")
	if rmErr := vault.Remove(probe); rmErr != nil {
		return false, rmErr
	}
	if errors.Is(err, iofs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
