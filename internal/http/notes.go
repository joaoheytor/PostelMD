// Package http holds the HTTP handlers and middleware, and nothing else (CLAUDE.md rule 1):
// no filesystem, no SQL. Handlers translate requests into calls on the layers below and
// their errors into status codes.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	stdhttp "net/http"
	"path"
	"regexp"
	"strings"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

// Notes is what the notes API needs from the vault (ADR-0020). *vault.Vault implements it.
type Notes interface {
	Read(p string) ([]byte, vault.Rev, error)
	List(p string) ([]vault.Entry, error)
	Save(ctx context.Context, p string, base vault.Rev, content []byte) (vault.Rev, error)
	Create(ctx context.Context, p string, content []byte) (vault.Rev, error)
	Mkdir(ctx context.Context, p string) error
	Move(ctx context.Context, from, to string) error
	Delete(ctx context.Context, p string) (string, error)
	Files(max int) ([]string, bool, error)
}

// maxFiles bounds the finder's file list: a vault far past it needs search, not a list.
const maxFiles = 50_000

// NotesAPI serves /api/v1/notes, /api/v1/folders, /api/v1/move and /api/v1/files:
//
//	GET    /api/v1/notes/{path}    the note's bytes, with its ETag
//	PUT    /api/v1/notes/{path}    If-Match: save; If-None-Match: * create
//	DELETE /api/v1/notes/{path}    move to the trash
//	GET    /api/v1/folders/{path}  the folder's entries ("" is the root)
//	POST   /api/v1/folders/{path}  create the folder
//	DELETE /api/v1/folders/{path}  move to the trash
//	POST   /api/v1/move            {"from": …, "to": …}; never replaces the destination
//	GET    /api/v1/files           every file's path, for the finder (at most maxFiles)
type NotesAPI struct {
	notes Notes
	log   *slog.Logger
}

// NewNotesAPI returns the notes API on a new mux.
func NewNotesAPI(notes Notes, log *slog.Logger) *stdhttp.ServeMux {
	a := &NotesAPI{notes: notes, log: log}
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("GET /api/v1/notes/{path...}", a.read)
	mux.HandleFunc("PUT /api/v1/notes/{path...}", a.write)
	mux.HandleFunc("DELETE /api/v1/notes/{path...}", a.delete)
	mux.HandleFunc("GET /api/v1/folders/{path...}", a.list)
	mux.HandleFunc("POST /api/v1/folders/{path...}", a.mkdir)
	mux.HandleFunc("DELETE /api/v1/folders/{path...}", a.delete)
	mux.HandleFunc("POST /api/v1/move", a.move)
	mux.HandleFunc("GET /api/v1/files", a.files)
	return mux
}

var etagRe = regexp.MustCompile(`^"([0-9a-f]{64})"$`)

func etag(r vault.Rev) string { return `"` + string(r) + `"` }

func (a *NotesAPI) read(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	p := r.PathValue("path")
	b, rev, err := a.notes.Read(p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeNote(w, stdhttp.StatusOK, p, b, rev)
}

// writeNote sends a note's bytes as data, never as a page: only .md is labelled as text,
// browsers may not sniff another type, and a sandboxing CSP stops anything that is still
// rendered from running or loading (threat model I1, I5).
func writeNote(w stdhttp.ResponseWriter, status int, p string, b []byte, rev vault.Rev) {
	ct := "application/octet-stream"
	if strings.EqualFold(path.Ext(p), ".md") {
		ct = "text/markdown; charset=utf-8"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	if rev != "" {
		h.Set("ETag", etag(rev))
	}
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(b) //nolint:gosec // G705: user content served as inert data; see the headers above
}

// write saves (If-Match) or creates (If-None-Match: *). Exactly one is required, so a
// client can't create a note by accident: an autosave always sends If-Match (ADR-0003 §7).
func (a *NotesAPI) write(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	ifMatch, ifNone := r.Header.Get("If-Match"), r.Header.Get("If-None-Match")
	if (ifMatch == "") == (ifNone == "") {
		writeError(w, stdhttp.StatusPreconditionRequired, "send exactly one of If-Match or If-None-Match: *")
		return
	}
	body, err := io.ReadAll(stdhttp.MaxBytesReader(w, r.Body, vault.MaxNoteSize))
	if err != nil {
		a.fail(w, r, bodyErr(err))
		return
	}
	p := r.PathValue("path")
	if ifNone != "" {
		if ifNone != "*" {
			writeError(w, stdhttp.StatusBadRequest, "If-None-Match must be *")
			return
		}
		rev, err := a.notes.Create(r.Context(), p, body)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		w.Header().Set("ETag", etag(rev))
		w.WriteHeader(stdhttp.StatusCreated)
		return
	}
	m := etagRe.FindStringSubmatch(ifMatch)
	if m == nil {
		writeError(w, stdhttp.StatusBadRequest, "If-Match must be one strong ETag")
		return
	}
	rev, err := a.notes.Save(r.Context(), p, vault.Rev(m[1]), body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	w.WriteHeader(stdhttp.StatusNoContent)
}

func bodyErr(err error) error {
	var tooBig *stdhttp.MaxBytesError
	if errors.As(err, &tooBig) {
		return vault.ErrTooLarge
	}
	return err
}

func (a *NotesAPI) delete(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	name, err := a.notes.Delete(r.Context(), r.PathValue("path"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string]string{"trashName": name})
}

func (a *NotesAPI) list(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	entries, err := a.notes.List(r.PathValue("path"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string][]vault.Entry{"entries": entries})
}

// files lists every file's path for the finder.
func (a *NotesAPI) files(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	files, truncated, err := a.notes.Files(maxFiles)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if files == nil {
		files = []string{}
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"files": files, "truncated": truncated})
}

func (a *NotesAPI) mkdir(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if err := a.notes.Mkdir(r.Context(), r.PathValue("path")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(stdhttp.StatusCreated)
}

func (a *NotesAPI) move(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.notes.Move(r.Context(), req.From, req.To); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

// fail maps a vault error to a response. Only the path is logged, never content.
func (a *NotesAPI) fail(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
	var conflict *vault.ConflictError
	switch {
	case errors.As(err, &conflict):
		// The current note, so the client can show the conflict view without another read.
		writeNote(w, stdhttp.StatusPreconditionFailed, r.PathValue("path"), conflict.Current, conflict.Rev)
	case errors.Is(err, vault.ErrNotFound):
		writeError(w, stdhttp.StatusNotFound, "not found")
	case errors.Is(err, vault.ErrExists):
		writeError(w, stdhttp.StatusConflict, "already exists")
	case errors.Is(err, vault.ErrIntoItself):
		writeError(w, stdhttp.StatusBadRequest, "a folder can't move into itself")
	case errors.Is(err, vault.ErrNameTooLong):
		writeError(w, stdhttp.StatusBadRequest, "a file or folder name can be at most 255 bytes")
	case errors.Is(err, vault.ErrTooLarge):
		writeError(w, stdhttp.StatusRequestEntityTooLarge, "note too large")
	case errors.Is(err, vault.ErrBusy):
		w.Header().Set("Retry-After", "1")
		writeError(w, stdhttp.StatusServiceUnavailable, "busy, retry")
	case errors.Is(err, context.Canceled):
		// The client went away; nothing to answer.
	default:
		// The route pattern is ours; the error can quote a note's path, so it is logged
		// without its text (rule 15).
		a.log.Error("notes API", "route", r.Pattern, pathless(err))
		writeError(w, stdhttp.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w stdhttp.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w stdhttp.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// oneLine escapes line breaks so a value from a request stays on its log line.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", `\n`)
	return strings.ReplaceAll(s, "\r", `\r`)
}

// decodeJSON reads a small JSON body into v, refusing other content types (a cross-site
// form can't send application/json without a preflight) and unknown fields. On failure it
// has already answered.
func decodeJSON(w stdhttp.ResponseWriter, r *stdhttp.Request, v any) bool {
	return decodeJSONMax(w, r, v, 8<<10)
}

// decodeJSONMax is decodeJSON for a body of at most limit bytes; a larger one is 413.
func decodeJSONMax(w stdhttp.ResponseWriter, r *stdhttp.Request, v any, limit int64) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, stdhttp.StatusUnsupportedMediaType, "send application/json")
		return false
	}
	dec := json.NewDecoder(stdhttp.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if _, ok := errors.AsType[*stdhttp.MaxBytesError](err); ok {
			writeError(w, stdhttp.StatusRequestEntityTooLarge, "the request body is too large")
			return false
		}
		writeError(w, stdhttp.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}
