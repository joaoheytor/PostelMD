//go:build linux

package sql

import (
	"errors"
	"sync"
	"sync/atomic"

	sqlite "modernc.org/sqlite"
)

// SQLite's primary result codes that make a failed write a writer fault (ADR-0030, "I/O
// errors"; ADR-0037).
const (
	codeIOErr = 10 // SQLITE_IOERR
	codeFull  = 13 // SQLITE_FULL
)

// ErrWriterFault: a write transaction failed with SQLITE_CORRUPT, SQLITE_IOERR or SQLITE_FULL,
// or an earlier one did. The operation failed and nothing retries the commit; from then on
// every write transaction of this DB is refused without touching the file, until a restart
// runs the checks at start (ADR-0030, ADR-0037).
var ErrWriterFault = errors.New("sql: the database couldn't be written (SQLITE_CORRUPT, SQLITE_IOERR or SQLITE_FULL); " +
	"nothing more is written until the server restarts (ADR-0030)")

// fault is a DB's writer fault: once set, it stays set, and the hook runs once.
type fault struct {
	set  atomic.Bool
	once sync.Once
	mu   sync.Mutex
	hook func()
}

// OnWriterFault sets hook to run once, the first time a write transaction fails with a writer
// fault (serve puts every vault read-only). It runs on the writing goroutine, after the
// transaction is rolled back and the writer connection given back; it must not block. Set it
// before the writes it should see.
func (db *DB) OnWriterFault(hook func()) {
	db.fault.mu.Lock()
	db.fault.hook = hook
	db.fault.mu.Unlock()
}

// WriterFaulted reports whether a write transaction has failed with a writer fault.
func (db *DB) WriterFaulted() bool { return db.fault.set.Load() }

// check is called with every write transaction's error: a writer fault sets the fault and
// fires the hook, and comes back wrapped with ErrWriterFault.
func (f *fault) check(err error) error {
	if err == nil || !isWriterFault(err) {
		return err
	}
	f.set.Store(true)
	f.fire()
	return errors.Join(ErrWriterFault, err)
}

func (f *fault) fire() {
	f.mu.Lock()
	hook := f.hook
	f.mu.Unlock()
	if hook != nil {
		f.once.Do(hook)
	}
}

// isWriterFault reports whether err carries SQLite's SQLITE_CORRUPT, SQLITE_IOERR or
// SQLITE_FULL (their extended codes too, such as SQLITE_IOERR_WRITE).
func isWriterFault(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() & 0xff {
	case codeCorrupt, codeIOErr, codeFull:
		return true
	}
	return false
}
