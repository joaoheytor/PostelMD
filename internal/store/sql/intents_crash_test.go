//go:build linux

package sql

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
)

var intentCaseFlag = flag.String("sql.intent-case", "", "the intent transaction the crash helper runs")

// intentCase is one of the intents' transactions: setup brings an assigned database to the
// state before it (in the test), run is the transaction (in the helper, killed at
// <point>-before-commit or <point>-after-commit).
type intentCase struct {
	point string
	setup func(ctx context.Context, db *DB) error
	run   func(ctx context.Context, db *DB) error
}

func rowAt(ctx context.Context, db *DB, path string) File { f, _ := db.FileByPath(ctx, path); return f }

func folderPayload(ctx context.Context, db *DB) DeleteIntent {
	under, _ := db.FilesUnder(ctx, "notes")
	p := DeleteIntent{Path: "notes", Folder: true, Device: 2049, Inode: 40, Mechanism: MechanismNoReplace}
	for _, f := range under {
		p.Files = append(p.Files, affected(f))
	}
	return p
}

func moveFolder(ctx context.Context, db *DB) MoveIntent {
	d := folderPayload(ctx, db)
	return MoveIntent{From: "notes", To: "archive", Folder: true, Device: 2049, Inode: 40, Files: d.Files, Mechanism: MechanismNoReplace}
}

var folderItem = NewTrashItem{Name: "notes", OriginalPath: "notes", Kind: "folder"}

func restoreFolder() RestoreIntent {
	op := anAssignment(t0).Trash[1].OpID
	return RestoreIntent{TrashOpID: op, Entry: BytePath(".postelmd-trash/" + op), To: "drafts", Folder: true, Mechanism: MechanismNoReplace}
}

func nothing(context.Context, *DB) error { return nil }

// applyAll applies units as a scan would, failing if any was skipped.
func applyAll(ctx context.Context, db *DB, units ...[]Change) error {
	applied, err := db.ApplyScan(ctx, t0, units)
	if err == nil && slices.Contains(applied, false) {
		err = fmt.Errorf("a setup unit was skipped: %v", applied)
	}
	return err
}

// staleIn leaves dir with what a completion must clear when an item lands there: a.md's row
// on dir/old.md (its file went since the last scan), and a hold on dir/held.md.
func staleIn(dir string) func(ctx context.Context, db *DB) error {
	return func(ctx context.Context, db *DB) error {
		a := rowAt(ctx, db, "a.md")
		err := applyAll(ctx, db, []Change{UpdateFile{Row: rowOf(a), NewPath: dir + "/old.md", SHA256: a.SHA256, Stat: a.Stat}},
			[]Change{InsertFile{Path: "x.md", SHA256: sum(8), Stat: FileStat{Inode: 80}}})
		if err != nil {
			return err
		}
		x := rowAt(ctx, db, "x.md")
		return applyAll(ctx, db, []Change{Suspect{Row: rowOf(x)},
			Hold{Path: dir + "/held.md", FileID: x.ID, SHA256: x.SHA256, Stat: FileStat{Size: 2, Inode: 99}}})
	}
}

// holdUnderNotes holds notes/held.md for a.md: a delete of notes releases it.
func holdUnderNotes(ctx context.Context, db *DB) error {
	a := rowAt(ctx, db, "a.md")
	return applyAll(ctx, db, []Change{Suspect{Row: rowOf(a)},
		Hold{Path: "notes/held.md", FileID: a.ID, SHA256: a.SHA256, Stat: FileStat{Size: 2, Inode: 98}}})
}

// then runs the setups in order, stopping at the first error.
// flagged is a setup step that flags the open intent opID.
func flagged(opID string) func(context.Context, *DB) error {
	return func(ctx context.Context, db *DB) error { return db.Flag(ctx, opID) }
}

func then(steps ...func(context.Context, *DB) error) func(context.Context, *DB) error {
	return func(ctx context.Context, db *DB) error {
		for _, s := range steps {
			if err := s(ctx, db); err != nil {
				return err
			}
		}
		return nil
	}
}

func intentCases() map[string]intentCase {
	save := func(ctx context.Context, db *DB) error { return db.Begin(ctx, opA, aSave(rowAt(ctx, db, "a.md")), t0) }
	// A suspect on the create's destination: the completion vacates it in the same
	// transaction.
	suspect := func(ctx context.Context, db *DB) error {
		_, err := db.ApplyScan(ctx, t0, [][]Change{{Suspect{Row: rowOf(rowAt(ctx, db, "a.md"))}}})
		return err
	}
	beginCreate := func(ctx context.Context, db *DB) error {
		return db.Begin(ctx, opA, CreateIntent{Path: "a.md", NewSHA256: sum(5)}, t0)
	}
	create := then(suspect, beginCreate)
	move := func(ctx context.Context, db *DB) error { return db.Begin(ctx, opA, moveFolder(ctx, db), t0) }
	del := func(ctx context.Context, db *DB) error {
		return db.BeginDelete(ctx, stamp, folderPayload(ctx, db), folderItem, t0)
	}
	restore := func(ctx context.Context, db *DB) error { return db.BeginRestore(ctx, opA, restoreFolder(), t0) }
	purged := anAssignment(t0).Trash[0].OpID
	return map[string]intentCase{
		"save-begin": {"save-begin", nothing, save},
		"save-complete": {"save-complete", save, func(ctx context.Context, db *DB) error {
			return db.CompleteSave(ctx, opA, FileStat{Size: 9, Inode: 90})
		}},
		"create-begin": {"create-begin", suspect, beginCreate},
		"create-complete": {"create-complete", create, func(ctx context.Context, db *DB) error {
			_, err := db.CompleteCreate(ctx, opA, FileStat{Size: 5, Inode: 50}, t0)
			return err
		}},
		"move-begin":      {"move-begin", nothing, move},
		"move-complete":   {"move-complete", then(staleIn("archive"), move), func(ctx context.Context, db *DB) error { return db.CompleteMove(ctx, opA, nil, t0) }},
		"delete-begin":    {"delete-begin", nothing, del},
		"delete-complete": {"delete-complete", then(holdUnderNotes, del), func(ctx context.Context, db *DB) error { return db.CompleteDelete(ctx, stamp, t0) }},
		"delete-drop":     {"drop", del, func(ctx context.Context, db *DB) error { return db.Drop(ctx, stamp) }},
		"restore-begin":   {"restore-begin", nothing, restore},
		"restore-complete": {"restore-complete", then(staleIn("drafts"), restore), func(ctx context.Context, db *DB) error {
			return db.CompleteRestore(ctx, opA, []NewFile{{Path: "drafts/x.md", SHA256: sum(6), Stat: FileStat{Inode: 60}}}, t0)
		}},
		"restore-drop": {"drop", restore, func(ctx context.Context, db *DB) error { return db.Drop(ctx, opA) }},
		"flag":         {"flag", save, func(ctx context.Context, db *DB) error { return db.Flag(ctx, opA) }},
		"save-resolve": {"resolve", then(save, flagged(opA)), func(ctx context.Context, db *DB) error { return db.Resolve(ctx, opA) }},
		"delete-resolve": {"resolve", then(del, flagged(stamp)), func(ctx context.Context, db *DB) error {
			return db.Resolve(ctx, stamp)
		}},
		"restore-resolve": {"resolve", then(restore, flagged(opA)), func(ctx context.Context, db *DB) error {
			return db.Resolve(ctx, opA)
		}},
		"purge-begin": {"purge-begin", nothing, func(ctx context.Context, db *DB) error { return db.BeginPurge(ctx, purged) }},
		"purge-drop": {"trash-drop", func(ctx context.Context, db *DB) error { return db.BeginPurge(ctx, purged) },
			func(ctx context.Context, db *DB) error { return db.DropTrashRecord(ctx, purged, "purging") }},
	}
}

// TestIntentHelper is a re-run of this test binary that runs one intent transaction on the
// database in -sql.crash-dir, SIGKILLed at -sql.crash-at.
func TestIntentHelper(t *testing.T) {
	if *intentCaseFlag == "" {
		t.Skip("run by TestIntentCrashPoints")
	}
	db, err := Open(context.Background(), *crashDir, discard)
	if err != nil {
		t.Fatal(err)
	}
	killPoint = func(p string) {
		if p == *crashAt {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {} // the signal is on its way
		}
	}
	err = intentCases()[*intentCaseFlag].run(context.Background(), db)
	t.Fatalf("not killed at %s (%v)", *crashAt, err)
}

// Every intent transaction is one commit (ADR-0033), so each has two rows: killed before the
// commit, app.db is exactly as before the transaction (recovery sees the intent, or no
// intent, as it was); killed after it, exactly as after a run that wasn't killed. "Exactly"
// compares every row of files, tombstones, pending, trash_records and intents, with new IDs
// (random) compared as new.
func TestIntentCrashPoints(t *testing.T) {
	cases := intentCases()
	for _, name := range slices.Sorted(maps.Keys(cases)) {
		c := cases[name]
		for _, committed := range []bool{false, true} {
			at := c.point + "-before-commit"
			if committed {
				at = c.point + "-after-commit"
			}
			t.Run(name+"/"+at, func(t *testing.T) {
				dir := setupIntentCase(t, c)
				before := digestOf(t, dir, "")
				want := before
				if committed {
					want = expectedAfter(t, dir, c, before)
				}
				cmd := exec.Command(os.Args[0], "-test.run=^TestIntentHelper$", "-test.count=1", //nolint:gosec // G204: this test binary, with its own flags
					"-sql.crash-at="+at, "-sql.crash-dir="+dir, "-sql.intent-case="+name)
				out, err := cmd.CombinedOutput()
				var ee *exec.ExitError
				if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					t.Fatalf("the helper wasn't killed: %v\n%s", err, out)
				}
				if got := digestOf(t, dir, before); got != want {
					t.Fatalf("after the kill:\n%s\nwant:\n%s", got, want)
				}
			})
		}
	}
}

// setupIntentCase makes an assigned database in a new state directory, brings it to the
// state before c's transaction, and closes it.
func setupIntentCase(t *testing.T, c intentCase) string {
	t.Helper()
	dir := closedDB(t)
	db, err := Open(context.Background(), dir, discard)
	must(t, err)
	must(t, db.Assign(context.Background(), anAssignment(t0)))
	must(t, c.setup(context.Background(), db))
	must(t, db.Close())
	return dir
}

// expectedAfter runs c's transaction, not killed, on a copy of dir and returns its digest.
func expectedAfter(t *testing.T, dir string, c intentCase, before string) string {
	t.Helper()
	cp := filepath.Join(t.TempDir(), "state")
	must(t, os.CopyFS(cp, os.DirFS(dir)))
	must(t, os.Chmod(cp, 0o700)) //nolint:gosec // G302: a folder, owner-only, as layout makes the state directory
	db, err := Open(context.Background(), cp, discard)
	must(t, err)
	must(t, c.run(context.Background(), db))
	must(t, db.Close())
	after := digestOf(t, cp, before)
	if after == before {
		t.Fatal("the transaction changed nothing: the case tests nothing")
	}
	return after
}

// anID is a file ID standing alone: a 64-digit hex hash is one longer word, so it never
// matches.
var anID = regexp.MustCompile(`\b[0-9A-HJKMNP-TV-Z]{26}\b`)

// digestOf lists every row of the intents' tables in dir's app.db, one per line, sorted; an
// ID that isn't in known (an earlier digest) reads NEW.
func digestOf(t *testing.T, dir, known string) string {
	t.Helper()
	db, err := Open(context.Background(), dir, discard)
	must(t, err)
	defer func() { _ = db.Close() }()
	var lines []string
	for _, q := range []string{
		"SELECT 'file', id, hex(path), generation, hex(sha256), size, device, inode, mtime_ns, ctime_ns, state, suspect_since_ms FROM files",
		"SELECT 'tomb', id, hex(last_path), last_generation, at_ms, reason FROM tombstones",
		"SELECT 'hold', hex(path), file_id, hex(sha256), inode FROM pending",
		"SELECT 'trash', op_id, hex(name), hex(original_path), kind, state, deleted_at_ms, clock_ms FROM trash_records",
		"SELECT 'intent', op_id, kind, state, payload, at_ms FROM intents",
	} {
		rows, err := db.r.Query(q)
		must(t, err)
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			must(t, rows.Scan(ptrs...))
			lines = append(lines, fmt.Sprintf("%v", vals))
		}
		must(t, rows.Err())
		_ = rows.Close()
	}
	for i, l := range lines {
		lines[i] = anID.ReplaceAllStringFunc(l, func(id string) string {
			if known != "" && !strings.Contains(known, id) {
				return "NEW"
			}
			return id
		})
	}
	slices.Sort(lines) // after the IDs are replaced: a new ID sorts as NEW
	return strings.Join(lines, "\n")
}
