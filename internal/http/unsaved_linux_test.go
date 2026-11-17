//go:build linux

package http

import (
	"bytes"
	"context"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/auth"
)

// TestUnsavedAuthChangeIsReported: a revocation or password change whose auth.json can't
// be saved (a real auth.Service on a read-only state directory) answers 500 saying so and
// what to do, clears the caller's (already revoked) cookie, and logs the error without the
// path it quotes (#187).
func TestUnsavedAuthChangeIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes read-only directories")
	}
	for _, tc := range []struct {
		method, path, body string
		want               []string
	}{
		{"POST", "/api/v1/session/revoke", "", []string{"log in and log out everywhere again"}},
		{"PUT", "/api/v1/password", `{"current":"` + password + `","next":"a brand new password"}`, []string{
			"log in with the new password and change it again",
			"if the server restarted before you repeated it, the old password is back: log in with it and change the password again",
		}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			dir := t.TempDir()
			svc, err := auth.OpenDir(dir, audit.New(slog.New(slog.DiscardHandler)), slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = svc.Close() })
			tok, _ := svc.NewSetupToken()
			cookie, err := svc.Setup(context.Background(), tok, password, "Ada", "ada@example.invalid")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // read-only, but still listable
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // removing a directory needs its x bit

			var logs bytes.Buffer
			mux := stdhttp.NewServeMux()
			(&AuthAPI{auth: svc, log: slog.New(slog.NewJSONHandler(&logs, nil))}).register(mux)
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Cookie", cookieName+"="+cookie)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			body := w.Body.String()
			if w.Code != stdhttp.StatusInternalServerError || !strings.Contains(body, "a restart would undo it") {
				t.Fatalf("answered %d %s, want 500 saying the change wasn't saved", w.Code, body)
			}
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Fatalf("answered %s, want it to say %q", body, want)
				}
			}
			if c := session(w.Result()); c == nil || c.MaxAge >= 0 {
				t.Fatalf("the revoked cookie wasn't cleared: %+v", c)
			}
			line := logs.String()
			if strings.Contains(line, dir) || strings.Count(line, "\n") != 1 ||
				!strings.Contains(line, `"msg":"auth change not saved"`) ||
				!strings.Contains(line, `"err":{"op":"create","errno":"EACCES","kind":"permission","type":"syscall.Errno"}`) {
				t.Fatalf("logged %s, want one pathless line naming the create, EACCES and syscall.Errno", line)
			}
		})
	}
}
