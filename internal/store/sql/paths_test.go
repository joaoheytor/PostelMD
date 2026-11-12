//go:build linux

package sql

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// emptyDir fails the test if dir holds anything.
func emptyDir(t *testing.T, dir, what string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("%s: %s was created", what, e.Name())
	}
}

// Relative paths are refused, never resolved: in a file: URI "localhost/<abs>" becomes an
// authority (Go would check ./localhost/<abs> while SQLite opens <abs>), and a VACUUM INTO
// target like "file:x.db" is a URI of its own.
func TestRelativePathsAreRefused(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	abs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "localhost"+abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cwd, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"localhost" + abs, "state", "./state"} {
		t.Run("Open "+dir, func(t *testing.T) {
			if db, err := Open(context.Background(), dir, discard); !errors.Is(err, ErrRelativePath) {
				if db != nil {
					_ = db.Close()
				}
				t.Fatalf("got %v, want ErrRelativePath", err)
			}
			emptyDir(t, abs, "the authority's path")
			emptyDir(t, filepath.Join(cwd, dir), "the relative state directory")
		})
	}
	t.Run("openPool", func(t *testing.T) {
		if p, err := openPool("localhost"+abs+"/app.db", pragmas, 1); !errors.Is(err, ErrRelativePath) {
			if p != nil {
				_ = p.Close()
			}
			t.Fatalf("got %v, want ErrRelativePath", err)
		}
		emptyDir(t, abs, "openPool")
	})
	db, _ := newDB(t)
	for _, dest := range []string{"copy.db", "file:x.db", "file:" + abs + "/x.db", "localhost" + abs + "/x.db"} {
		t.Run("Snapshot "+dest, func(t *testing.T) {
			if _, err := db.Snapshot(context.Background(), dest); !errors.Is(err, ErrRelativePath) {
				t.Fatalf("got %v, want ErrRelativePath", err)
			}
			for _, f := range []string{"copy.db", "x.db", "file:x.db", "file:"} {
				if _, err := os.Lstat(filepath.Join(cwd, f)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s exists in the working directory: %v", f, err)
				}
			}
			emptyDir(t, abs, "the snapshot's absolute part")
		})
	}
}

// Every sql.Open of the driver goes through openPool, so the ATTACH policy's connection hook
// is installed before any connection opens; and only this package imports the driver.
func TestOnlyOpenPoolOpensSQLite(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) && p != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p) //nolint:gosec // G304: the repository's own sources
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if bytes.Contains(b, []byte(`sql.Open("sqlite"`)) && rel != filepath.Join("internal", "store", "sql", "conn.go") {
			t.Errorf("%s opens SQLite without openPool", rel)
		}
		if bytes.Contains(b, []byte(`"modernc.org/sqlite"`)) && filepath.Dir(rel) != filepath.Join("internal", "store", "sql") {
			t.Errorf("%s imports the SQLite driver outside internal/store/sql", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
