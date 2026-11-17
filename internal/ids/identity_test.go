//go:build linux

package ids

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// TestTheIdentitySampleIsDeterministic: the same rows give the same sample in any order,
// suspects are left out, and at most 1,000 rows are taken (ADR-0030, ADR-0040).
func TestTheIdentitySampleIsDeterministic(t *testing.T) {
	var rows []sqlstore.File
	for i := range 3000 { // 2,000 live rows and 1,000 suspect ones
		rows = append(rows, sqlstore.File{ID: fmt.Sprintf("id%04d", i), Path: fmt.Sprintf("p%d", i), Suspect: i%3 == 0})
	}
	a := sample(testVaultUUID, rows)
	slices.Reverse(rows)
	b := sample(testVaultUUID, rows)
	if len(a) != 1000 || !slices.EqualFunc(a, b, func(x, y sqlstore.File) bool { return x.ID == y.ID }) {
		t.Fatalf("two samples of the same rows differ (sizes %d, %d)", len(a), len(b))
	}
	for _, r := range a {
		if r.Suspect {
			t.Fatalf("the sample has the suspect row %s", r.ID)
		}
	}
	ids := func(s []sqlstore.File) []string {
		out := make([]string, len(s))
		for i, r := range s {
			out[i] = r.ID
		}
		slices.Sort(out)
		return out
	}
	other := sample("00000000-0000-4000-8000-000000000000", rows)
	if len(other) != 1000 || slices.Equal(ids(a), ids(other)) {
		t.Fatal("two vault UUIDs choose the same rows: the sample doesn't depend on the vault UUID")
	}
}

// TestTheIdentityCheck: every row matching passes; a vault where no row matches fails; the
// 90% line is inclusive; a row whose file has another inode or size, or is a symlink, doesn't
// match; a database with no live rows passes.
func TestTheIdentityCheck(t *testing.T) {
	files := map[string]string{}
	for i := range 20 {
		files[fmt.Sprintf("n%02d.md", i)] = fmt.Sprintf("note %d", i)
	}
	e := newEnv(t, files)
	check := func(want Identity) {
		t.Helper()
		got, err := CheckIdentity(ctx, e.db, e.dir)
		must(t, err)
		if got != want {
			t.Fatalf("CheckIdentity = %+v, want %+v", got, want)
		}
	}
	check(Identity{Sampled: 20, Matched: 20, Files: 20, Rows: 20})
	e.write("n00.md", "a longer note 0") // another size
	e.rm("n01.md")
	e.cp("n02.md", "c")
	e.rm("n02.md")
	e.mv("c", "n02.md") // another inode
	check(Identity{Sampled: 20, Matched: 17, Files: 19, Rows: 20})
	if (Identity{Sampled: 20, Matched: 18}).Passes() != true || (Identity{Sampled: 20, Matched: 17}).Passes() {
		t.Fatal("the 90% line is wrong")
	}
	e.rm("n03.md")
	must(t, os.Symlink("n04.md", filepath.Join(e.dir, "n03.md")))
	check(Identity{Sampled: 20, Matched: 16, Files: 18, Rows: 20}) // a symlink is neither a file nor a match

	other := t.TempDir() // another vault: nothing matches
	must(t, os.WriteFile(filepath.Join(other, "x.md"), []byte("x"), 0o600))
	got, err := CheckIdentity(ctx, e.db, other)
	must(t, err)
	if got.Matched != 0 || got.Passes() {
		t.Fatalf("another vault: %+v, passes %v", got, got.Passes())
	}

	empty := newEnv(t, nil)
	if got, err := CheckIdentity(ctx, empty.db, empty.dir); err != nil || !got.Passes() || got.Sampled != 0 {
		t.Fatalf("no rows: %+v, %v", got, err)
	}
}

// TestInspectChangesNothing: the identity check opens neither the vault nor its files: a
// folder without the marker or temp folder stays without them.
func TestInspectChangesNothing(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("a"), 0o600))
	in, err := vault.Inspect(dir, []string{"a.md", "missing.md", ".postelmd-trash/x"})
	must(t, err)
	if in.Files != 1 || len(in.Stats) != 1 {
		t.Fatalf("Inspect = %+v", in)
	}
	if _, err := os.Lstat(filepath.Join(dir, vault.TmpDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Inspect made the temp folder: %v", err)
	}
}
