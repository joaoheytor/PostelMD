package http

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"log/slog"
	"syscall"
)

// pathless describes err with values that can't carry user text, so no path or name it
// quotes reaches the log (CLAUDE.md rule 15) and no line break can forge a log line: the
// store operation that failed (one of ours, by name), the system error's name when it is a
// known one, a fixed kind, and the Go type of the innermost error. The first three are picked
// from fixed lists by comparison, never copied out of err (CodeQL go/log-injection). Every API
// logs its unexpected errors with it.
func pathless(err error) slog.Attr {
	op := "unknown"
	var pe *iofs.PathError
	if errors.As(err, &pe) {
		// Our own name for the operation, never pe.Op itself: an Op the store doesn't use is
		// logged as "unknown".
		for _, name := range []string{"open", "stat", "rename", "remove", "list", "mkdir", "sync", "read", "write", "create"} {
			if pe.Op == name {
				op = name
			}
		}
	}
	// Compared, never copied out of err, so nothing from it flows into the log line.
	errno := "none"
	for _, e := range []struct {
		errno syscall.Errno
		name  string
	}{{syscall.EIO, "EIO"}, {syscall.ENOSPC, "ENOSPC"}, {syscall.EDQUOT, "EDQUOT"}, {syscall.EACCES, "EACCES"},
		{syscall.EPERM, "EPERM"}, {syscall.EROFS, "EROFS"}, {syscall.ENOTEMPTY, "ENOTEMPTY"}, {syscall.EBUSY, "EBUSY"},
		{syscall.EXDEV, "EXDEV"}, {syscall.ELOOP, "ELOOP"}, {syscall.ENAMETOOLONG, "ENAMETOOLONG"},
		{syscall.EMFILE, "EMFILE"}, {syscall.EINVAL, "EINVAL"}} {
		if errors.Is(err, e.errno) {
			errno = e.name
			break
		}
	}
	kind := "unexpected"
	for _, k := range []struct {
		err  error
		name string
	}{{iofs.ErrNotExist, "not exist"}, {iofs.ErrExist, "exists"}, {iofs.ErrPermission, "permission"},
		{context.DeadlineExceeded, "deadline"}} {
		if errors.Is(err, k.err) {
			kind = k.name
			break
		}
	}
	return slog.Group("err", "op", op, "errno", errno, "kind", kind, "type", innermostType(err))
}

// innermostType names the Go type of the error at the bottom of err's chain (for example
// *json.SyntaxError), so an operator can tell failures apart. A type name comes from code,
// never from user text.
func innermostType(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return fmt.Sprintf("%T", err)
		}
		err = next
	}
}
