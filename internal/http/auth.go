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
)

// Auth is what the HTTP layer needs from authentication. *auth.Service implements it.
type Auth interface {
	NeedsSetup() bool
	Setup(ctx context.Context, token, password, name, email string) (string, error)
	Login(ctx context.Context, ip, password string) (string, error)
	Check(cookie string) error
	ChangePassword(ctx context.Context, ip, cookie, current, next string) (string, error)
	LogoutEverywhere(ctx context.Context, cookie string) error
}

// Cookie names. Over TLS the __Host- prefix pins the cookie to this host, with Secure and
// Path=/ (ADR-0005).
const (
	cookieName    = "postelmd_session"
	hostCookieKey = "__Host-postelmd_session"
)

// AuthAPI serves:
//
//	POST   /api/v1/setup             {token, password, name, email}: first run only
//	GET    /api/v1/session           204 with a valid session, else 401
//	POST   /api/v1/session           {password}: log in
//	DELETE /api/v1/session           log out this browser
//	POST   /api/v1/session/revoke    log out everywhere
//	PUT    /api/v1/password          {current, next}
type AuthAPI struct {
	auth Auth
	log  *slog.Logger
}

func (a *AuthAPI) register(mux *stdhttp.ServeMux) {
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
	w.WriteHeader(stdhttp.StatusCreated)
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
	switch {
	case errors.As(err, &wait):
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Wait.Seconds()))))
		writeError(w, stdhttp.StatusTooManyRequests, "too many attempts, retry later")
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
