package http

import (
	"context"
	"errors"
	iofs "io/fs"
	"log/slog"
	stdhttp "net/http"
	"syscall"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

// Trash is what the trash API needs from the vault (ADR-0024). *vault.Vault implements it.
type Trash interface {
	ListTrash(max int) ([]vault.TrashItem, bool, error)
	RestoreTrashed(ctx context.Context, id, folder, name string) (string, error)
}

// maxTrash bounds the trash listing: the newest items, as many as a person reads through.
const maxTrash = 1_000

// TrashAPI serves:
//
//	GET  /api/v1/trash               the newest items, {items: [{id, name, kind, deletedAt,
//	                                 size}], truncated}
//	POST /api/v1/trash/{id}/restore  {folder, name}: move the item back; never replaces
//	                                 anything (409). The id is a trash entry's name;
//	                                 410 when no such item is in the trash.
type TrashAPI struct {
	trash Trash
	notes *NotesAPI // for its error mapping
	log   *slog.Logger
}

func (a *TrashAPI) register(mux *stdhttp.ServeMux) {
	mux.HandleFunc("GET /api/v1/trash", a.list)
	mux.HandleFunc("POST /api/v1/trash/{id}/restore", a.restore)
}

func (a *TrashAPI) list(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	items, truncated, err := a.trash.ListTrash(maxTrash)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"items": items, "truncated": truncated})
}

func (a *TrashAPI) restore(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req struct {
		Folder string `json:"folder"` // "" for the vault root
		Name   string `json:"name"`   // "" for the item's own name
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := a.trash.RestoreTrashed(r.Context(), r.PathValue("id"), req.Folder, req.Name)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string]string{"path": p})
}

// fail answers with plain messages the web app shows as they are. An unexpected error is
// logged without the paths it may quote: trashed names are user content (CLAUDE.md rule 15).
func (a *TrashAPI) fail(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
	switch {
	case errors.Is(err, vault.ErrNotInTrash):
		// Gone, not Not Found: the app tells it from a missing folder by the status alone.
		writeError(w, stdhttp.StatusGone, "that item is no longer in the trash")
	case errors.Is(err, vault.ErrNameHasSlash):
		writeError(w, stdhttp.StatusBadRequest, "a name can't contain /")
	case errors.Is(err, vault.ErrNotFound):
		writeError(w, stdhttp.StatusNotFound, "that folder doesn't exist")
	case errors.Is(err, vault.ErrExists):
		writeError(w, stdhttp.StatusConflict, "something with that name is already there; choose another name")
	case errors.Is(err, vault.ErrNameTooLong), errors.Is(err, vault.ErrBusy), errors.Is(err, context.Canceled):
		a.notes.fail(w, r, err)
	default:
		a.log.Error("trash API", "route", r.Pattern, pathless(err))
		writeError(w, stdhttp.StatusInternalServerError, "internal error")
	}
}

// pathless describes err with values that can't carry user text, so no path or name it
// quotes reaches the log (CLAUDE.md rule 15) and no line break can forge a log line: the
// store operation that failed (one of ours, by name), the system error's name when it is a
// known one, and a fixed kind. Each is picked from a fixed list by comparison, never copied
// out of err (CodeQL go/log-injection).
func pathless(err error) slog.Attr {
	op := "unknown"
	var pe *iofs.PathError
	if errors.As(err, &pe) {
		// Our own name for the operation, never pe.Op itself: an Op the store doesn't use is
		// logged as "unknown".
		for _, name := range []string{"open", "rename", "remove", "list", "mkdir", "sync", "read", "write", "create"} {
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
	return slog.Group("err", "op", op, "errno", errno, "kind", kind)
}
