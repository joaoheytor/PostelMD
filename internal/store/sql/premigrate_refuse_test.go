//go:build linux

package sql

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// files reads every file in dir except app.db's -wal and -shm, which a refused open may
// create: a regular file by its bytes, a symlink by its target, anything else by its type.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, e := range es {
		n := e.Name()
		if n == AppDB+"-wal" || n == AppDB+"-shm" {
			continue
		}
		if e.Type()&fs.ModeSymlink != 0 {
			l, _ := os.Readlink(filepath.Join(dir, n))
			m[n] = "link " + l
			continue
		}
		if !e.Type().IsRegular() {
			m[n] = "type " + e.Type().String()
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n)) //nolint:gosec // G304: the test's own files
		if err != nil {
			t.Fatal(err)
		}
		m[n] = string(b)
	}
	return m
}

// ADR-0015's "anything else" row, and the states around it a migration never leaves: each
// is refused with ErrMigrateInterference, changing nothing (the database, the intent, the
// snapshot and its temp all stay as they were).
func TestMigrateInterferenceIsRefused(t *testing.T) {
	later, from := laterMigrations(t, 2)
	to := from + 2
	in := intent{From: from, To: to, Snapshot: snapshotName(from, to)}
	snap := snapshotName(from, to)
	one, _ := laterMigrations(t, 1)
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir string) // dir holds a closed database at from
	}{
		{"between from and to, the snapshot absent", func(t *testing.T, dir string) {
			openWith(t, dir, one)
			mustRemove(t, filepath.Join(dir, snapshotName(from, from+1))) // that run's own, not this intent's
			writeIntentFile(t, dir, in)
		}},
		{"between from and to, the snapshot at another version", func(t *testing.T, dir string) {
			openWith(t, dir, one)
			mustRemove(t, filepath.Join(dir, snapshotName(from, from+1)))
			snapshotAt(t, one, filepath.Join(dir, snap))
			writeIntentFile(t, dir, in)
		}},
		{"at to, the snapshot absent", func(t *testing.T, dir string) {
			openWith(t, dir, later)
			mustRemove(t, filepath.Join(dir, snap))
			writeIntentFile(t, dir, in)
		}},
		{"below from, a snapshot present", func(t *testing.T, dir string) {
			in := intent{From: from + 1, To: to, Snapshot: snapshotName(from+1, to)}
			snapshotAt(t, one, filepath.Join(dir, in.Snapshot))
			writeIntentFile(t, dir, in)
		}},
		{"at from, the snapshot damaged", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, snap), []byte("not a database"))
			writeIntentFile(t, dir, in)
		}},
		{"at from, the snapshot at another version", func(t *testing.T, dir string) {
			snapshotAt(t, later, filepath.Join(dir, snap))
			writeIntentFile(t, dir, in)
		}},
		{"at from, the snapshot a symlink", func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "copy.db")
			snapshotAt(t, embeddedFS(t), target)
			mustSymlink(t, target, filepath.Join(dir, snap))
			writeIntentFile(t, dir, in)
		}},
		{"at from, the snapshot and its temp both present", func(t *testing.T, dir string) {
			snapshotAt(t, embeddedFS(t), filepath.Join(dir, snap))
			writeFile(t, filepath.Join(dir, snap+".tmp"), []byte("partial"))
			writeIntentFile(t, dir, in)
		}},
		{"an intent written for another build's last migration", func(t *testing.T, dir string) {
			writeIntentFile(t, dir, intent{From: from, To: from + 1, Snapshot: snapshotName(from, from+1)})
		}},
		{"an intent naming another snapshot", func(t *testing.T, dir string) {
			writeIntentFile(t, dir, intent{From: from, To: to, Snapshot: "../" + snap})
		}},
		{"an intent that doesn't parse", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, MigrateIntent), []byte(`{"from": 1, "to"`))
		}},
		{"an intent with unknown fields", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, MigrateIntent),
				fmt.Appendf(nil, `{"from":%d,"to":%d,"snapshot":%q,"extra":1}`, from, to, snap))
		}},
		{"an intent that is a symlink", func(t *testing.T, dir string) {
			other := t.TempDir()
			writeIntentFile(t, other, in)
			mustSymlink(t, filepath.Join(other, MigrateIntent), filepath.Join(dir, MigrateIntent))
		}},
		{"no intent, the snapshot already there", func(t *testing.T, dir string) {
			snapshotAt(t, embeddedFS(t), filepath.Join(dir, snap))
		}},
		{"no intent, the snapshot's temp already there", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, snap+".tmp"), []byte("partial"))
		}},
		{"an intent, and app.db lost", func(t *testing.T, dir string) {
			mustRemove(t, filepath.Join(dir, AppDB))
			snapshotAt(t, embeddedFS(t), filepath.Join(dir, snap))
			writeIntentFile(t, dir, in)
		}},
		// A file SQLite would open beside the snapshot: a FIFO there would block it for good.
		{"at from, a FIFO at the snapshot's -journal", func(t *testing.T, dir string) {
			snapshotAt(t, embeddedFS(t), filepath.Join(dir, snap))
			mustMkfifo(t, filepath.Join(dir, snap+"-journal"))
			writeIntentFile(t, dir, in)
		}},
		{"at from, a file at the snapshot's -journal", func(t *testing.T, dir string) {
			snapshotAt(t, embeddedFS(t), filepath.Join(dir, snap))
			writeFile(t, filepath.Join(dir, snap+"-journal"), []byte("planted"))
			writeIntentFile(t, dir, in)
		}},
		{"between from and to, a FIFO at the snapshot's -journal", func(t *testing.T, dir string) {
			openWith(t, dir, one)
			mustRemove(t, filepath.Join(dir, snapshotName(from, from+1)))
			snapshotAt(t, embeddedFS(t), filepath.Join(dir, snap))
			mustMkfifo(t, filepath.Join(dir, snap+"-journal"))
			writeIntentFile(t, dir, in)
		}},
		{"at to, a FIFO at the snapshot's -journal", func(t *testing.T, dir string) {
			openWith(t, dir, later) // nothing opens the snapshot at to: only the early check sees it
			mustMkfifo(t, filepath.Join(dir, snap+"-journal"))
			writeIntentFile(t, dir, in)
		}},
		{"at from, a FIFO at the snapshot's -wal", func(t *testing.T, dir string) {
			snapshotAt(t, embeddedFS(t), filepath.Join(dir, snap))
			mustMkfifo(t, filepath.Join(dir, snap+"-wal"))
			writeIntentFile(t, dir, in)
		}},
		{"no intent, a FIFO at the snapshot's -journal", func(t *testing.T, dir string) {
			mustMkfifo(t, filepath.Join(dir, snap+"-journal"))
		}},
		{"at from, a FIFO at the snapshot temp's -journal", func(t *testing.T, dir string) {
			mustMkfifo(t, filepath.Join(dir, snap+".tmp-journal"))
			writeIntentFile(t, dir, in)
		}},
		{"no intent, the snapshot temp's -journal already there", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, snap+".tmp-journal"), []byte("stale"))
		}},
		// No step leaves a -wal or -shm beside the snapshot's temp (VACUUM INTO's output and
		// the read-only check use a rollback journal): refused with or without an intent.
		{"no intent, a file at the snapshot temp's -wal", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, snap+".tmp-wal"), []byte("planted"))
		}},
		{"no intent, a file at the snapshot temp's -shm", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, snap+".tmp-shm"), []byte("planted"))
		}},
		{"at from, a file at the snapshot temp's -wal", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, snap+".tmp-wal"), []byte("planted"))
			writeIntentFile(t, dir, in)
		}},
		{"at from, a temp and a FIFO at its -shm", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, snap+".tmp"), []byte("partial"))
			mustMkfifo(t, filepath.Join(dir, snap+".tmp-shm"))
			writeIntentFile(t, dir, in)
		}},
		// The post_migration_started file is checked before the first side effect, not after
		// the migrations committed.
		{"at from, a folder at the post_migration_started file", func(t *testing.T, dir string) {
			mustMkdir(t, filepath.Join(dir, markerName(from, to)))
			writeIntentFile(t, dir, in)
		}},
		{"at from, a symlink at the post_migration_started file", func(t *testing.T, dir string) {
			mustSymlink(t, filepath.Join(t.TempDir(), "marker"), filepath.Join(dir, markerName(from, to)))
			writeIntentFile(t, dir, in)
		}},
		{"no intent, a folder at the post_migration_started file", func(t *testing.T, dir string) {
			mustMkdir(t, filepath.Join(dir, markerName(from, to)))
		}},
		{"no intent, a symlink at the post_migration_started file", func(t *testing.T, dir string) {
			mustSymlink(t, filepath.Join(t.TempDir(), "marker"), filepath.Join(dir, markerName(from, to)))
		}},
		// readIntent's guards.
		{"an intent with trailing data", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, MigrateIntent),
				fmt.Appendf(nil, `{"from":%d,"to":%d,"snapshot":%q}{}`, from, to, snap))
		}},
		{"an intent from 0", func(t *testing.T, dir string) {
			writeIntentFile(t, dir, intent{From: 0, To: to, Snapshot: snapshotName(0, to)})
		}},
		{"an intent with to not above from", func(t *testing.T, dir string) {
			writeIntentFile(t, dir, intent{From: from, To: from, Snapshot: snapshotName(from, from)})
		}},
		{"a folder at the intent", func(t *testing.T, dir string) {
			mustMkdir(t, filepath.Join(dir, MigrateIntent))
		}},
		{"a FIFO at the intent", func(t *testing.T, dir string) {
			mustMkfifo(t, filepath.Join(dir, MigrateIntent))
		}},
		{"an intent over the size cap", func(t *testing.T, dir string) {
			b := fmt.Appendf(nil, `{"from":%d,"to":%d,"snapshot":%q}`, from, to, snap)
			writeFile(t, filepath.Join(dir, MigrateIntent), append(b, bytes.Repeat([]byte(" "), intentMax)...))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := closedDB(t)
			c.setup(t, dir)
			before := files(t, dir)
			_, had := before[AppDB]
			db, err := openGuarded(t, dir, later)
			if !errors.Is(err, ErrMigrateInterference) {
				if db != nil {
					_ = db.Close()
				}
				t.Fatalf("got %v, want ErrMigrateInterference", err)
			}
			after := files(t, dir)
			if !had {
				delete(after, AppDB) // the open created an empty one, and refused the intent
			}
			for n, b := range before {
				if a, ok := after[n]; !ok || a != b {
					t.Fatalf("a refused open changed %s", n)
				}
			}
			if len(after) != len(before) {
				t.Fatalf("a refused open left files: %v, was %v", keys(after), keys(before))
			}
		})
	}
}

func keys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
