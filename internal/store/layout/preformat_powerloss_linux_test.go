//go:build linux

package layout

import (
	"errors"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/store/fs"
	"github.com/joaoheytor/PostelMD/internal/store/fs/faultfs"
)

// TestCopyPreFormat2SurvivesAPowerLossAtEveryStep cuts the power at each step of step 1,
// from a state directory with an incomplete earlier copy (faultfs's model: a file's data
// survives once synced, a directory's entries once the directory is synced). Whatever
// survives: a complete file means every source survived in full; the earlier copy is never
// lost; and once CopyPreFormat2 returns, the whole copy is durable.
func TestCopyPreFormat2SurvivesAPowerLossAtEveryStep(t *testing.T) {
	sources := map[string]string{formatFile: "1\n", "auth.json": `{"hash":"x"}`, "secret.key": "key"}
	for crashAt := 0; ; crashAt++ {
		state := v01State(t)
		copyOf(t, state, "stale", nil)
		disk, err := faultfs.New(state, crashAt)
		must(t, err)
		_, err = CopyPreFormat2(state, stamp, tag, nil, fs.WithHook(disk))
		if err != nil && !errors.Is(err, faultfs.ErrCrashed) {
			t.Fatalf("crash at step %d: %v", crashAt, err)
		}
		after := disk.AfterPowerLoss()
		complete, ok := after[PreFormat2+"/complete"]
		if !disk.Crashed() && !ok {
			t.Fatalf("CopyPreFormat2 returned, but complete isn't durable")
		}
		if ok {
			if string(complete) != tag {
				t.Fatalf("crash at step %d: complete survived as %q", crashAt, complete)
			}
			for n, want := range sources {
				if got, ok := after[PreFormat2+"/"+n]; !ok || string(got) != want {
					t.Fatalf("crash at step %d: complete survived, but %s as %q (there %v)", crashAt, n, got, ok)
				}
			}
		}
		if string(after[PreFormat2+"/auth.json"]) != "stale" && string(after[PreFormat2+"-"+stamp+"/auth.json"]) != "stale" {
			t.Fatalf("crash at step %d: the earlier copy is lost: %v", crashAt, keys(after))
		}
		if !disk.Crashed() {
			if crashAt < 16 {
				t.Fatalf("step 1 took %d steps; want the move and its sync, the folder and its sync, three copies "+
					"(create, sync, rename), the folder's sync, complete (create, sync) and the folder's sync", crashAt)
			}
			return
		}
	}
}

// recorder is an fs.Hook that lists the steps.
type recorder struct{ ops []fs.Op }

func (r *recorder) Before(fs.Op) error { return nil }

func (r *recorder) After(op fs.Op, _ error) { r.ops = append(r.ops, op) }

// TestCopyPreFormat2SyncsTheCopiesBeforeComplete: faultfs makes a directory's entries
// durable together, so it can't see a filesystem that persists complete's entry before the
// renames that came first. The order is asserted on the steps instead: the folder is synced
// after the last copy's rename and before complete is created, and again after.
func TestCopyPreFormat2SyncsTheCopiesBeforeComplete(t *testing.T) {
	state := v01State(t)
	rec := &recorder{}
	_, err := CopyPreFormat2(state, stamp, tag, nil, fs.WithHook(rec))
	must(t, err)
	lastRename, synced, created := -1, -1, -1
	for i, op := range rec.ops {
		switch {
		case op.Kind == fs.OpRename:
			lastRename = i
		case op.Kind == fs.OpSyncDir && op.Path == PreFormat2 && created < 0:
			synced = i
		case op.Kind == fs.OpCreate && op.Path == PreFormat2+"/complete":
			created = i
		}
	}
	if ordered := lastRename < synced && synced < created; !ordered {
		t.Fatalf("last rename at %d, folder sync at %d, complete at %d: want them in that order", lastRename, synced, created)
	}
	if last := rec.ops[len(rec.ops)-1]; last.Kind != fs.OpSyncDir || last.Path != PreFormat2 {
		t.Fatalf("the last step is %+v, want the folder's sync", last)
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
