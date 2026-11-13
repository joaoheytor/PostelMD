//go:build linux

// Package lock is the instance lock (ADR-0014 §1, CLAUDE.md rule 12): one exclusive,
// non-blocking flock on /data/.postelmd/instance.lock, held for the process lifetime. A held
// lock is never broken.
package lock

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// HeldError: another process holds the lock.
type HeldError struct{ Holder string }

func (e *HeldError) Error() string {
	return "lock: another PostelMD process is using this data directory (" + e.Holder + ")"
}

// Lock is a held instance lock.
type Lock struct {
	f    *os.File
	boot string
}

// Acquire takes the lock at path and records this process in it (hostname, PID, start
// time, boot ID).
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0o600) //nolint:gosec // G304: the state directory's own lock file
	if err != nil {
		return nil, fmt.Errorf("lock: open %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil { //nolint:gosec // G115: an fd fits in int
		holder, _ := io.ReadAll(io.LimitReader(f, 512))
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, &HeldError{Holder: string(holder)}
		}
		return nil, fmt.Errorf("lock: flock %s: %w", path, err)
	}
	host, _ := os.Hostname()
	boot := newBootUUID()
	info := fmt.Sprintf("host=%s pid=%d started=%s boot=%s", host, os.Getpid(),
		time.Now().UTC().Format(time.RFC3339), boot)
	if err := f.Truncate(0); err == nil {
		_, err = f.WriteAt([]byte(info), 0)
		if err == nil {
			err = f.Sync()
		}
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock: record holder: %w", err)
		}
	}
	return &Lock{f: f, boot: boot}, nil
}

// newBootUUID is a random version-4 UUID (crypto/rand.Read never fails).
func newBootUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// BootUUID is this start's boot UUID, recorded in the lock file and, by serve, in app.db
// (ADR-0014).
func (l *Lock) BootUUID() string { return l.boot }

// Release drops the lock.
func (l *Lock) Release() error { return l.f.Close() }
