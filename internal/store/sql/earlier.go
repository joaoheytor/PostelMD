//go:build linux

package sql

import (
	"context"
	"io/fs"
	"log/slog"
	"strconv"
)

// LastMigration is this build's last migration's number: the database version it writes.
func LastMigration() (int, error) {
	ms, err := loadMigrations(migrationFiles)
	return len(ms), err
}

// OpenEarlier opens (creating if absent) stateDir/app.db with only this build's first n
// migrations, as the build whose last migration was n did. Tests of other packages use it to
// set up an earlier build's database (the db rollback command's test); nothing else does.
func OpenEarlier(ctx context.Context, stateDir string, n int, log *slog.Logger) (*DB, error) {
	return open(ctx, stateDir, log, firstMigrations{FS: migrationFiles, n: n})
}

// firstMigrations is a migrations folder that lists only the first n files. Embedding the
// fs.FS interface keeps only Open, so fs.Glob reads the folder through ReadDir below.
type firstMigrations struct {
	fs.FS
	n int
}

func (f firstMigrations) ReadDir(name string) ([]fs.DirEntry, error) {
	es, err := fs.ReadDir(f.FS, name)
	if err != nil {
		return nil, err
	}
	out := es[:0]
	for _, e := range es {
		if m := migrationName.FindStringSubmatch(e.Name()); m != nil {
			if v, _ := strconv.Atoi(m[1]); v <= f.n {
				out = append(out, e)
			}
		}
	}
	return out, nil
}
