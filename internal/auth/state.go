package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"strings"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// Files in the state directory (/data/.postelmd).
const (
	credentialFile = "auth.json"
	secretFile     = "secret.key"
	tmpPrefix      = "auth.json.tmp-"
	secretLen      = 32
)

// credential is the whole of auth.json (ADR-0020). Epoch revokes every session when bumped.
type credential struct {
	PasswordHash string `json:"passwordHash"`
	Epoch        uint64 `json:"epoch"`
	Name         string `json:"name"`  // git author name
	Email        string `json:"email"` // git author email
}

// loadCredential returns the stored credential, or nil before setup. Leftover temp files
// from an interrupted write are removed first.
func loadCredential(root *fs.Root) (*credential, error) {
	entries, err := root.ReadDir(".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			if err := root.Remove(e.Name()); err != nil {
				return nil, err
			}
		}
	}
	b, err := root.ReadFile(credentialFile)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c credential
	if err := json.Unmarshal(b, &c); err != nil || c.PasswordHash == "" {
		return nil, fmt.Errorf("auth: %s is damaged; reset the password with `postelmd reset-password`", credentialFile)
	}
	return &c, nil
}

// saveCredential replaces auth.json atomically: a synced temp file renamed over it, then
// the directory synced. One rename, so no intent (CLAUDE.md rule 14).
func saveCredential(root *fs.Root, c *credential) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	tmp := tmpPrefix + hex.EncodeToString(id[:])
	if err := root.CreateFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := root.Rename(tmp, credentialFile); err != nil {
		return errors.Join(err, root.Remove(tmp))
	}
	return root.SyncDir(".")
}

// loadOrCreateSecret returns the instance secret, creating it (0600, durably) on first run.
// It is never regenerated: that would log everyone out.
func loadOrCreateSecret(root *fs.Root) ([]byte, error) {
	for range 2 {
		b, err := root.ReadFile(secretFile)
		if err == nil {
			if len(b) != secretLen {
				return nil, fmt.Errorf("auth: %s has %d bytes, want %d", secretFile, len(b), secretLen)
			}
			return b, nil
		}
		if !errors.Is(err, iofs.ErrNotExist) {
			return nil, err
		}
		key := make([]byte, secretLen)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		err = root.CreateFile(secretFile, key, 0o600)
		if errors.Is(err, iofs.ErrExist) {
			continue // created concurrently; read it
		}
		if err != nil {
			return nil, err
		}
		return key, root.SyncDir(".")
	}
	return nil, fmt.Errorf("auth: can't create %s", secretFile)
}
