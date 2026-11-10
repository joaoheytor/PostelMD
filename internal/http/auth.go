package http

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	stdhttp "net/http"
	"strconv"

	"github.com/joaoheytor/PostelMD/internal/auth"
	"github.com/joaoheytor/PostelMD/internal/vault"
)

// Auth is what the HTTP layer needs from authentication. *auth.Service implements it.
type Auth interface {
	NeedsSetup() bool
	Setup(ctx context.Context, token, password, name, email string) (string, error)
	Login(ctx context.Context, ip, password string) (string, error)
	Check(cookie string) error
	ChangePassword(ctx context.Context, ip, cookie, current, next string) (string, error)
	LogoutEverywhere(ctx context.Context, cookie string) error
	CheckSetupToken(ctx context.Context, token string) error
	Author() (name, email string)
}

// Importer makes the first commit when setup adopts a folder (ADR-0020).
type Importer interface {
	PendingImport(ctx context.Context) (int, error)
	ImportInitial(ctx context.Context, name, email string) (vault.Import, error)
}

// Cookie names. Over TLS the __Host- prefix pins the cookie to this host, with Secure and
// Path=/ (ADR-0005).
const (
	cookieName    = "postelmd_session"
	hostCookieKey = "__Host-postelmd_session"
)

// AuthAPI serves:
//
//	POST   /api/v1/setup/check       {token}: the token is valid; {files} the import will commit
//	POST   /api/v1/setup             {token, password, name, email}: first run only; {imported, skipped}
//	GET    /api/v1/session           204 with a valid session, else 401
//	POST   /api/v1/session           {password}: log in
//	DELETE /api/v1/session           log out this browser
//	POST   /api/v1/session/revoke    log out everywhere
//	PUT    /api/v1/password          {current, next}
type AuthAPI struct {
	auth     Auth
	importer Importer
	log      *slog.Logger
}

func (a *AuthAPI) register(mux *stdhttp.ServeMux) {
	mux.HandleFunc("POST /api/v1/setup/check", a.checkSetup)
	mux.HandleFunc("POST /api/v1/setup", a.setup)
	mux.HandleFunc("GET /api/v1/session", a.session)
	mux.HandleFunc("POST /api/v1/session", a.login)
	mux.HandleFunc("DELETE /api/v1/session", a.logout)
	mux.HandleFunc("POST /api/v1/session/revoke", a.revoke)
	mux.HandleFunc("PUT /api/v1/password", a.password)
}

// sessionCookie returns the request's session cookie value, or "".
func sessionCookie(r *stdhttp.Request) string {
	name := cookieName
	if r.TLS != nil {
		name = hostCookieKey
	}
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

func setSession(w stdhttp.ResponseWriter, r *stdhttp.Request, value string) {
	c := &stdhttp.Cookie{
		Name: cookieName, Value: value, Path: "/", HttpOnly: true, SameSite: stdhttp.SameSiteLaxMode,
		MaxAge: int(auth.SessionTTL.Seconds()),
	}
	if r.TLS != nil {
		c.Name, c.Secure = hostCookieKey, true
	}
	if value == "" {
		c.MaxAge = -1
	}
	stdhttp.SetCookie(w, c)
}

// clientIP is the TCP peer. The MVP trusts no proxy headers (TRUSTED_PROXIES is empty).
func clientIP(r *stdhttp.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RequireSession answers 401 unless the request carries a valid session. It runs on every
// request (CLAUDE.md rule 16).
func RequireSession(a Auth, next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if a.Check(sessionCookie(r)) != nil {
			writeError(w, stdhttp.StatusUnauthorized, "log in")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *AuthAPI) setup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req struct{ Token, Password, Name, Email string }
	if !decodeJSON(w, r, &req) {
		return
	}
	cookie, err := a.auth.Setup(r.Context(), req.Token, req.Password, req.Name, req.Email)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	setSession(w, r, cookie)
	// An adopted folder's files become the first commit (ADR-0020). Setup already
	// succeeded; if the import fails, the files stay uncommitted changes the user commits.
	imp, err := a.importer.ImportInitial(r.Context(), req.Name, req.Email)
	if err != nil {
		a.log.Error("initial import", "err", oneLine(err.Error()))
	}
	// skipped: files over the commit size limit, left out of git; the notice says so.
	writeJSON(w, stdhttp.StatusCreated, struct {
		Imported int `json:"imported"`
		Skipped  int `json:"skipped,omitempty"`
	}{imp.Files, len(imp.Skipped)})
}

// checkSetup lets the setup page check the token and show how many files the setup will
// commit before the user confirms (ADR-0020). Only the token holder learns the count.
func (a *AuthAPI) checkSetup(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req struct{ Token string }
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.auth.CheckSetupToken(r.Context(), req.Token); err != nil {
		a.fail(w, r, err)
		return
	}
	n, err := a.importer.PendingImport(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string]int{"files": n})
}

func (a *AuthAPI) session(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if a.auth.Check(sessionCookie(r)) != nil {
		writeError(w, stdhttp.StatusUnauthorized, "log in")
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (a *AuthAPI) login(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req struct{ Password string }
	if !decodeJSON(w, r, &req) {
		return
	}
	cookie, err := a.auth.Login(r.Context(), clientIP(r), req.Password)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	setSession(w, r, cookie)
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (a *AuthAPI) logout(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	setSession(w, r, "")
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (a *AuthAPI) revoke(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if err := a.auth.LogoutEverywhere(r.Context(), sessionCookie(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	setSession(w, r, "")
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (a *AuthAPI) password(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req struct{ Current, Next string }
	if !decodeJSON(w, r, &req) {
		return
	}
	cookie, err := a.auth.ChangePassword(r.Context(), clientIP(r), sessionCookie(r), req.Current, req.Next)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	setSession(w, r, cookie)
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (a *AuthAPI) fail(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
	var wait *auth.WaitError
	if errors.As(err, &wait) {
		// Also on the failure that starts the wait, so the form can say how long it is.
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Wait.Seconds()))))
	}
	switch {
	case wait != nil && !wait.Checked:
		writeError(w, stdhttp.StatusTooManyRequests, "too many attempts, retry later")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, stdhttp.StatusServiceUnavailable, "request cancelled")
	case errors.Is(err, auth.ErrBadSession), errors.Is(err, auth.ErrBadPassword), errors.Is(err, auth.ErrNotSetUp):
		writeError(w, stdhttp.StatusUnauthorized, "wrong password or session")
	case errors.Is(err, auth.ErrBadToken):
		writeError(w, stdhttp.StatusForbidden, "invalid or expired setup token")
	case errors.Is(err, auth.ErrAlreadySetUp):
		writeError(w, stdhttp.StatusConflict, "already set up")
	case errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrBadProfile):
		writeError(w, stdhttp.StatusUnprocessableEntity, err.Error())
	default:
		a.log.Error("auth API", "route", r.Pattern, "err", oneLine(err.Error()))
		writeError(w, stdhttp.StatusInternalServerError, "internal error")
	}
}
