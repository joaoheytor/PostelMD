//go:build linux

package vault

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/git"
)

// listed is what the commit dialog sends back: each change's path and revision (none for a
// deletion).
func listed(st *Status) []Listed {
	out := []Listed{}
	for _, c := range st.Changes {
		l := Listed{Path: c.Path}
		if c.Kind != Deleted {
			l.Rev = revOf(c.Rev)
		}
		out = append(out, l)
	}
	return out
}

func revOf(r Rev) *Rev { return &r }

// commitAll commits what a status lists, as the commit dialog does. Status only reads, so
// the crash tests' steps are the commit's own.
func commitAll(v *Vault, msg, name, email string) (git.Hash, error) {
	st, err := v.Status(ctx)
	if err != nil {
		return git.Hash{}, err
	}
	return v.Commit(ctx, msg, name, email, listed(st))
}

// changedVault is a vault with one commit (notes/a.md = "A", c.md = "C") and then
// notes/a.md edited, b.md added and c.md deleted, all uncommitted.
func changedVault(t *testing.T, opts ...fs.Option) (*Vault, string) {
	t.Helper()
	v, dir := newVault(t, opts...)
	must(t, os.WriteFile(filepath.Join(dir, "c.md"), []byte("C"), 0o600))
	_, err := v.EnableGit()
	must(t, err)
	_, err = v.ImportInitial(ctx, "Ada", "ada@example.invalid")
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, "notes", "a.md"), []byte("A2"), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "b.md"), []byte("B"), 0o600))
	must(t, os.Remove(filepath.Join(dir, "c.md")))
	return v, dir
}

func TestStatusListsEachChangesRevision(t *testing.T) {
	v, _ := changedVault(t)
	st, err := v.Status(ctx)
	must(t, err)
	want := []Change{
		{Path: "b.md", Kind: Added, Rev: RevOf([]byte("B"))},
		{Path: "c.md", Kind: Deleted},
		{Path: "notes/a.md", Kind: Modified, Rev: RevOf([]byte("A2"))},
	}
	if !reflect.DeepEqual(st.Changes, want) {
		t.Fatalf("status = %+v, want %+v", st.Changes, want)
	}
}

func TestCommitTakesExactlyTheListedChanges(t *testing.T) {
	v, dir := changedVault(t)
	st, err := v.Status(ctx)
	must(t, err)
	_, err = v.Commit(ctx, "listed", "Ada", "ada@example.invalid", listed(st))
	must(t, err)
	if got := gitCLI(t, dir, "ls-tree", "-r", "--name-only", "HEAD"); got != "b.md\nnotes/a.md" {
		t.Fatalf("committed files = %q", got)
	}
	if got := gitCLI(t, dir, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("git sees changes after the commit: %q", got)
	}
}

// TestCommitRefusesAnotherSet: any difference between the listed set and the changes at
// commit time refuses the whole commit; nothing is committed, not even part of it.
func TestCommitRefusesAnotherSet(t *testing.T) {
	cases := []struct {
		name string
		edit func(t *testing.T, dir string, l []Listed) []Listed
	}{
		{"a file changed after the status", func(t *testing.T, dir string, l []Listed) []Listed {
			must(t, os.WriteFile(filepath.Join(dir, "notes", "a.md"), []byte("A3"), 0o600))
			return l
		}},
		{"an untracked file added after the status", func(t *testing.T, dir string, l []Listed) []Listed {
			must(t, os.WriteFile(filepath.Join(dir, "new.md"), []byte("N"), 0o600))
			return l
		}},
		{"a listed path no longer changed", func(t *testing.T, dir string, l []Listed) []Listed {
			must(t, os.Remove(filepath.Join(dir, "b.md")))
			return l
		}},
		{"a deletion missing from the list", func(_ *testing.T, _ string, l []Listed) []Listed {
			var out []Listed
			for _, c := range l {
				if c.Rev != nil {
					out = append(out, c)
				}
			}
			return out
		}},
		{"a deletion listed with a revision", func(_ *testing.T, _ string, l []Listed) []Listed {
			out := append([]Listed{}, l...)
			out[1].Rev = revOf(RevOf([]byte("C")))
			return out
		}},
		{"a change listed as a deletion", func(_ *testing.T, _ string, l []Listed) []Listed {
			out := append([]Listed{}, l...)
			out[0].Rev = nil
			return out
		}},
		{"a path listed twice", func(_ *testing.T, _ string, l []Listed) []Listed {
			return append(append([]Listed{}, l...), l[0])
		}},
		{"an extra path listed", func(_ *testing.T, _ string, l []Listed) []Listed {
			return append(append([]Listed{}, l...), Listed{Path: "z.md", Rev: revOf(RevOf([]byte("Z")))})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, dir := changedVault(t)
			st, err := v.Status(ctx)
			must(t, err)
			l := tc.edit(t, dir, listed(st))
			head := gitCLI(t, dir, "rev-parse", "HEAD")
			index := gitCLI(t, dir, "ls-files", "--stage")
			_, err = v.Commit(ctx, "stale", "Ada", "ada@example.invalid", l)
			if !errors.Is(err, ErrChangesDiffer) {
				t.Fatalf("commit = %v, want ErrChangesDiffer", err)
			}
			if got := gitCLI(t, dir, "rev-parse", "HEAD"); got != head {
				t.Fatalf("HEAD moved to %s", got)
			}
			if got := gitCLI(t, dir, "ls-files", "--stage"); got != index {
				t.Fatalf("the index changed:\n%s\nwant\n%s", got, index)
			}
		})
	}
}

func TestCommitWithNothingListedAndNothingChanged(t *testing.T) {
	v, _ := newVault(t)
	_, err := v.EnableGit()
	must(t, err)
	_, err = v.ImportInitial(ctx, "Ada", "ada@example.invalid")
	must(t, err)
	if _, err := v.Commit(ctx, "x", "Ada", "a@b", []Listed{}); !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("commit = %v, want ErrNothingToCommit", err)
	}
}

// TestCommitChecksUnderTheCommitsLock: the check and the commit hold one acquisition of the
// vault mutex and use one read of the files. Right after the check passes, a save can't get
// in, and a host edit made then isn't what gets committed: the commit has the checked bytes.
func TestCommitChecksUnderTheCommitsLock(t *testing.T) {
	v, dir := changedVault(t)
	v.wait = 20 * time.Millisecond
	st, err := v.Status(ctx)
	must(t, err)
	var saveErr error
	ran := false
	v.afterCheck = func() {
		ran = true
		_, saveErr = v.Save(ctx, "notes/a.md", RevOf([]byte("A2")), []byte("A3"))
		must(t, os.WriteFile(filepath.Join(dir, "b.md"), []byte("B2"), 0o600)) // the host
	}
	_, err = v.Commit(ctx, "listed", "Ada", "a@b", listed(st))
	must(t, err)
	if !ran {
		t.Fatal("the check never passed")
	}
	if !errors.Is(saveErr, ErrBusy) {
		t.Fatalf("save right after the check = %v, want ErrBusy", saveErr)
	}
	if got := gitCLI(t, dir, "show", "HEAD:b.md"); got != "B" {
		t.Fatalf("HEAD:b.md = %q, want the checked bytes", got)
	}
	if got := gitCLI(t, dir, "show", "HEAD:notes/a.md"); got != "A2" {
		t.Fatalf("HEAD:notes/a.md = %q, want the checked bytes", got)
	}
	if got := changes(t, v); len(got) != 1 || got[0].Path != "b.md" || got[0].Kind != Modified {
		t.Fatalf("after the commit, changes = %+v; want only the host's edit of b.md", got)
	}
}
