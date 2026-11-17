//go:build linux

package ids

import (
	"bytes"
	"context"
	"crypto/sha256"
	"slices"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// sampleSize is how many live rows the identity check looks at (ADR-0030).
const sampleSize = 1000

// Identity is the identity check's answer (ADR-0030, mechanics in ADR-0040): how many live
// rows it sampled and how many it found at their path with the same inode and size, and how
// many files the vault holds.
type Identity struct {
	Sampled, Matched, Files int
}

// Passes reports whether at least 90% of the sample matched; an empty sample passes.
func (c Identity) Passes() bool { return c.Matched*10 >= c.Sampled*9 }

// CheckIdentity runs the identity check of the database's rows against the vault at dir. It
// changes nothing: it opens neither the vault nor any file in it.
func CheckIdentity(ctx context.Context, db *sqlstore.DB, dir string, opts ...fs.Option) (Identity, error) {
	id, err := db.Identity(ctx)
	if err != nil {
		return Identity{}, err
	}
	rows, err := db.Files(ctx)
	if err != nil {
		return Identity{}, err
	}
	s := sample(id.VaultUUID, rows)
	paths := make([]string, len(s))
	for i, r := range s {
		paths[i] = r.Path
	}
	in, err := vault.Inspect(dir, paths, opts...)
	if err != nil {
		return Identity{}, err
	}
	c := Identity{Sampled: len(s), Files: in.Files}
	for _, r := range s {
		if st, ok := in.Stats[r.Path]; ok && st.Inode == r.Stat.Inode && st.Size == r.Stat.Size {
			c.Matched++
		}
	}
	return c, nil
}

// sample is the identity check's deterministic sample: the live rows ordered by SHA-256 of
// the vault UUID, a zero byte and the row's ID, the first sampleSize taken.
func sample(vaultUUID string, rows []sqlstore.File) []sqlstore.File {
	type keyed struct {
		key [sha256.Size]byte
		row sqlstore.File
	}
	ks := make([]keyed, 0, len(rows))
	for _, r := range rows {
		if !r.Suspect {
			ks = append(ks, keyed{sha256.Sum256([]byte(vaultUUID + "\x00" + r.ID)), r})
		}
	}
	slices.SortFunc(ks, func(a, b keyed) int { return bytes.Compare(a.key[:], b.key[:]) })
	out := make([]sqlstore.File, 0, min(len(ks), sampleSize))
	for _, k := range ks[:min(len(ks), sampleSize)] {
		out = append(out, k.row)
	}
	return out
}
