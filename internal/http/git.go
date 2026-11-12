package http

import (
	"context"
	"errors"
	"log/slog"
	stdhttp "net/http"
	"strconv"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

// Git is what the git API needs from the vault (ADR-0020). *vault.Vault implements it.
type Git interface {
	Status(ctx context.Context) (*vault.Status, error)
	GitLocks() ([]vault.LockInfo, error)
	Commit(ctx context.Context, msg, name, email string, listed []vault.Listed) (vault.Hash, error)
	History(p string, limit int) ([]vault.Version, error)
	Version(p, commit string) ([]byte, error)
	Restore(ctx context.Context, p, commit string, base vault.Rev) (vault.Rev, error)
	RemoveGitLocks(ctx context.Context) error
}

// Author is where commits take their author from: the one user's profile (ADR-0020).
type Author interface {
	Author() (name, email string)
}

// GitAPI serves:
//
//	GET  /api/v1/git/status                  {branch, changes, skipped, repositories, locks}
//	POST /api/v1/git/commit                  {message, changes: [{path, rev}]}: commit the
//	                                         changes if they are still exactly those
//	POST /api/v1/git/locks/remove            {confirmNoOtherGit: true}
//	GET  /api/v1/history/{path}              the commits that changed the note
//	GET  /api/v1/versions/{commit}/{path}    the note's bytes in that commit
//	POST /api/v1/restore                     {path, commit} with If-Match, through the save path
type GitAPI struct {
	git    Git
	author Author
	notes  *NotesAPI // for its error mapping and the conflict body
	log    *slog.Logger
}

func (a *GitAPI) register(mux *stdhttp.ServeMux) {
	mux.HandleFunc("GET /api/v1/git/status", a.status)
	mux.HandleFunc("POST /api/v1/git/commit", a.commit)
	mux.HandleFunc("POST /api/v1/git/locks/remove", a.removeLocks)
	mux.HandleFunc("GET /api/v1/history/{path...}", a.history)
	mux.HandleFunc("GET /api/v1/versions/{commit}/{path...}", a.version)
	mux.HandleFunc("POST /api/v1/restore", a.restore)
}

func (a *GitAPI) status(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	st, err := a.git.Status(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	locks, err := a.git.GitLocks()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, struct {
		*vault.Status
		Locks []vault.LockInfo `json:"locks"`
	}{st, append([]vault.LockInfo{}, locks...)})
}

// maxCommitBody bounds a commit request, whose list grows with the number of changes: it
// fits about 50,000 changes with 64-character paths.
const maxCommitBody = 8 << 20

// commit takes the changes the dialog listed (and scanned for secrets), and commits them
// only if they are still exactly the changes. The web app is the only client (ADR-0020).
func (a *GitAPI) commit(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req struct {
		Message string          `json:"message"`
		Changes *[]vault.Listed `json:"changes"`
	}
	if !decodeJSONMax(w, r, &req, maxCommitBody) {
		return
	}
	if req.Changes == nil {
		writeError(w, stdhttp.StatusUnprocessableEntity, "send the listed changes with the commit")
		return
	}
	name, email := a.author.Author()
	c, err := a.git.Commit(r.Context(), req.Message, name, email, *req.Changes)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, map[string]string{"commit": c.String()})
}

// removeLocks needs the user's explicit confirmation in the body: PostelMD can't tell a
// crash's leftover lock from a git command running on the host (ADR-0020).
func (a *GitAPI) removeLocks(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req struct {
		Confirm bool `json:"confirmNoOtherGit"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Confirm {
		writeError(w, stdhttp.StatusUnprocessableEntity, "confirm that no other git command is running")
		return
	}
	if err := a.git.RemoveGitLocks(r.Context()); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (a *GitAPI) history(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 500 {
			writeError(w, stdhttp.StatusBadRequest, "limit must be 1 to 500")
			return
		}
		limit = n
	}
	versions, err := a.git.History(r.PathValue("path"), limit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string][]vault.Version{"versions": append([]vault.Version{}, versions...)})
}

// version sends an old version as inert data, without an ETag: it is not the note's
// current revision, so a client can't mistake it for an If-Match base.
func (a *GitAPI) version(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	p := r.PathValue("path")
	b, err := a.git.Version(p, r.PathValue("commit"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeNote(w, stdhttp.StatusOK, p, b, "")
}

func (a *GitAPI) restore(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	m := etagRe.FindStringSubmatch(r.Header.Get("If-Match"))
	if m == nil {
		writeError(w, stdhttp.StatusPreconditionRequired, "send If-Match with the note's current ETag")
		return
	}
	var req struct {
		Path   string `json:"path"`
		Commit string `json:"commit"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	rev, err := a.git.Restore(r.Context(), req.Path, req.Commit, vault.Rev(m[1]))
	var conflict *vault.ConflictError
	if errors.As(err, &conflict) {
		writeConflict(w, req.Path, conflict)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (a *GitAPI) fail(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
	switch {
	case errors.Is(err, vault.ErrChangesDiffer):
		writeJSON(w, stdhttp.StatusConflict, map[string]string{"code": "changes-differ",
			"error": "the changes were updated since they were listed; review them again"})
	case errors.Is(err, vault.ErrNothingToCommit):
		writeError(w, stdhttp.StatusConflict, "nothing to commit")
	case errors.Is(err, vault.ErrBadMessage):
		writeError(w, stdhttp.StatusUnprocessableEntity, "the commit message must not be empty")
	case errors.Is(err, vault.ErrGitLocked):
		writeError(w, stdhttp.StatusLocked, "a git lock is blocking commits")
	case errors.Is(err, vault.ErrGitMoved):
		writeError(w, stdhttp.StatusConflict, "the branch moved during the commit, retry")
	case errors.Is(err, vault.ErrDetached):
		writeError(w, stdhttp.StatusConflict, "git HEAD is detached; check out a branch on the host")
	case errors.Is(err, vault.ErrUnsafeExclude):
		writeError(w, stdhttp.StatusConflict, ".git/info/exclude is a symlink or not a regular file; "+
			"PostelMD can't read it safely: replace it with a regular file on the host")
	default:
		a.notes.fail(w, r, err)
	}
}
