package http

import (
	"bytes"
	"context"
	"fmt"
	iofs "io/fs"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/auth"
)

// unsaved is an Auth whose password change and "log out everywhere" took effect but
// couldn't save auth.json.
type unsaved struct {
	Auth
	err error
}

func (u unsaved) ChangePassword(context.Context, string, string, string, string) (string, error) {
	return "", u.err
}

func (u unsaved) LogoutEverywhere(context.Context, string) error { return u.err }

// TestUnsavedAuthChangeIsReported: a revocation or password change that wasn't saved
// answers 500 saying so and what to do, clears the caller's (already revoked) cookie, and
// logs the error without the path it quotes (#187).
func TestUnsavedAuthChangeIsReported(t *testing.T) {
	secret := "state-" + strings.Repeat("q7", 4)
	err := fmt.Errorf("%w: %w", auth.ErrNotSaved,
		&iofs.PathError{Op: "create", Path: secret + "/auth.json.tmp-1", Err: syscall.ENOSPC})
	for _, tc := range []struct{ method, path, body, want string }{
		{"POST", "/api/v1/session/revoke", "", "log in and log out everywhere again"},
		{"PUT", "/api/v1/password", `{"current":"a","next":"b"}`, "log in with the new password and change it again"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var logs bytes.Buffer
			mux := stdhttp.NewServeMux()
			(&AuthAPI{auth: unsaved{err: err}, log: slog.New(slog.NewJSONHandler(&logs, nil))}).register(mux)
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != stdhttp.StatusInternalServerError || !strings.Contains(w.Body.String(), "a restart would undo it") ||
				!strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("answered %d %s, want 500 saying the change wasn't saved", w.Code, w.Body)
			}
			if c := session(w.Result()); c == nil || c.MaxAge >= 0 {
				t.Fatalf("the revoked cookie wasn't cleared: %+v", c)
			}
			line := logs.String()
			if strings.Contains(line, secret) || !strings.Contains(line, `"msg":"auth change not saved"`) ||
				!strings.Contains(line, `"errno":"ENOSPC"`) {
				t.Fatalf("logged %s, want one pathless line naming ENOSPC", line)
			}
		})
	}
}
