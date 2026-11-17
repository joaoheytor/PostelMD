//go:build linux

package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
)

// movedMarker, in the damaged folder, records that the original move is done: from then on,
// any database in the state directory is a half-built one and goes to attempt-<n>/. It is
// written after the moves are durable, and is the only signal step 2 trusts for that
// (CLAUDE.md rule 14): what happens to be in the folder isn't.
const movedMarker = "moved"

// MoveAsideForRebuild is db rebuild's step 2 for the intent r. It reports built: app.db
// already carries r's UUID (step 4 committed), so the rebuild goes on at step 5. Otherwise,
// until the damaged folder records the original move, it moves r.Moves there and records it;
// after that, a half-built database (app.db and every file SQLite keeps beside it) goes into
// the next attempt-<n>/ in it. Never over a moved file.
func MoveAsideForRebuild(ctx context.Context, stateDir string, r Rebuild) (built bool, err error) {
	d := filepath.Join(stateDir, r.DamagedDir())
	db := filepath.Join(stateDir, AppDB)
	cur, err := regularFile(db)
	if err != nil {
		return false, rebuildInterference(err)
	}
	recorded, err := regularFile(filepath.Join(d, movedMarker))
	if err != nil {
		return false, rebuildInterference(err)
	}
	if cur {
		if built, err = carriesRebuild(ctx, db, r.UUID); err != nil {
			return false, fmt.Errorf("sql: read the app.db beside rebuild.intent: %w", err)
		}
	}
	original := len(r.Moves) > 0
	if built {
		orig, err := regularFile(filepath.Join(d, AppDB))
		if err == nil && original && !recorded || err == nil && r.HadDB && !orig {
			err = errors.New("app.db is rebuilt, but the moved database isn't in " + r.DamagedDir())
		}
		if err != nil {
			return false, rebuildInterference(err)
		}
		return true, nil
	}
	if original && !recorded {
		return false, moveOriginal(stateDir, d, r.Moves)
	}
	return false, moveAttempt(stateDir, d, cur)
}

// moveOriginal moves each of moves still in stateDir into d, then records the move in d.
// A name in neither place, or a database file in stateDir the intent didn't list, is
// interference.
func moveOriginal(stateDir, d string, moves []string) error {
	for _, n := range dbFiles {
		if ok, err := regularFile(filepath.Join(stateDir, n)); err != nil || ok && !slices.Contains(moves, n) {
			return rebuildInterference(errors.Join(fmt.Errorf("%s is in the state directory, but rebuild.intent didn't list it", n), err))
		}
	}
	if err := mkdirDurable(d); err != nil {
		return err
	}
	for _, n := range moves {
		here, err := regularFile(filepath.Join(stateDir, n))
		if err != nil {
			return rebuildInterference(err)
		}
		if !here {
			if there, err := regularFile(filepath.Join(d, n)); err != nil || !there {
				return rebuildInterference(errors.Join(fmt.Errorf("%s is in neither the state directory nor %s", n, filepath.Base(d)), err))
			}
			continue
		}
		if err := move(stateDir, d, n); err != nil {
			return err
		}
	}
	if err := syncBoth(filepath.Join(stateDir, AppDB), filepath.Join(d, AppDB)); err != nil {
		return err
	}
	f, err := createExclusive(filepath.Join(d, movedMarker), "the move's record")
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = syncDir(filepath.Join(d, movedMarker))
	}
	killPoint("rebuild-moved-recorded")
	return err
}

// moveAttempt moves a half-built database's files from stateDir into attempt-<n>/ in d: a
// new folder when app.db is there (cur), else the folder its app.db already went to.
func moveAttempt(stateDir, d string, cur bool) error {
	if found, err := anyDBFile(stateDir); err != nil || !found {
		return err
	}
	if err := mkdirDurable(d); err != nil {
		return err
	}
	to, err := attemptDir(d, cur)
	if err != nil {
		return err
	}
	for _, n := range dbFiles {
		ok, err := regularFile(filepath.Join(stateDir, n))
		if err != nil {
			return rebuildInterference(err)
		}
		if ok {
			if err := move(stateDir, to, n); err != nil {
				return err
			}
		}
	}
	return syncBoth(filepath.Join(stateDir, AppDB), filepath.Join(to, AppDB))
}

// move renames stateDir/n to to/n, never over a file there.
func move(stateDir, to, n string) error {
	if err := renameNoReplace(filepath.Join(stateDir, n), filepath.Join(to, n)); errors.Is(err, os.ErrExist) {
		return rebuildInterference(fmt.Errorf("%s is already in %s", n, filepath.Base(to)))
	} else if err != nil {
		return fmt.Errorf("sql: move %s: %w", n, err)
	}
	killPoint("rebuild-moved")
	return nil
}

// anyDBFile reports whether app.db or a file SQLite keeps beside it is in stateDir.
func anyDBFile(stateDir string) (bool, error) {
	for _, n := range dbFiles {
		if ok, err := regularFile(filepath.Join(stateDir, n)); err != nil {
			return false, rebuildInterference(err)
		} else if ok {
			return true, nil
		}
	}
	return false, nil
}

// attemptDir is the attempt folder in d a half-built database moves to. With fresh (app.db
// still in the state directory), the one after the last holding an app.db; one a crash made
// before anything moved into it is that one, and is used again. Otherwise (files left by a
// move killed after app.db, which moves first), the last holding an app.db.
func attemptDir(d string, fresh bool) (string, error) {
	last := 0
	for n := 1; ; n++ {
		a := filepath.Join(d, "attempt-"+strconv.Itoa(n))
		fi, err := os.Lstat(a)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil || !fi.IsDir() {
			return "", rebuildInterference(errors.Join(fmt.Errorf("%s isn't a folder", filepath.Base(a)), err))
		}
		has, err := regularFile(filepath.Join(a, AppDB))
		if err != nil {
			return "", rebuildInterference(err)
		}
		if !has {
			break
		}
		last = n
	}
	if fresh {
		a := filepath.Join(d, "attempt-"+strconv.Itoa(last+1))
		return a, mkdirDurable(a)
	}
	if last == 0 {
		return "", rebuildInterference(errors.New("a database file is in the state directory without app.db, and no attempt folder holds its app.db"))
	}
	return filepath.Join(d, "attempt-"+strconv.Itoa(last)), nil
}

// carriesRebuild reports whether the database at path has uuid as its rebuild UUID, reading
// its committed state without changing it. A file that isn't a database, or has no meta
// table, is half-built (false); any other failure to read it is an error, never a guess.
func carriesRebuild(ctx context.Context, path, uuid string) (bool, error) {
	q := copyQuery
	if wal, err := regularFile(path + "-wal"); err != nil || !wal {
		q += "&immutable=1" // no WAL: read the file alone, creating no -wal or -shm
	}
	p, err := openPool(path, q, 1)
	if errors.Is(damaged(err), ErrDamaged) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = p.Close() }()
	var tables int
	err = p.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'").Scan(&tables)
	if errors.Is(damaged(err), ErrDamaged) {
		return false, nil
	}
	if err != nil || tables == 0 {
		return false, err
	}
	var v string
	err = p.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", rebuildUUIDKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && v == uuid, err
}
