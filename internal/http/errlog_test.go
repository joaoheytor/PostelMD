package http

import (
	"bytes"
	"context"
	"errors"
	iofs "io/fs"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/joaoheytor/PostelMD/internal/vault"
)

// importFails is an Auth whose setup succeeds and an Importer whose import fails; every other
// method is unused by the setup handler.
type importFails struct {
	Auth
	err error
}

func (f importFails) Setup(context.Context, string, string, string, string) (string, error) {
	return "cookie", nil
}

func (f importFails) PendingImport(context.Context) (int, error) { return 0, nil }

func (f importFails) ImportInitial(context.Context, string, string) (vault.Import, error) {
	return vault.Import{}, f.err
}

// TestUnexpectedErrorsLogNoPaths: the notes and auth APIs log an unexpected error without
// the path or text it quotes (CLAUDE.md rule 15), on one line (#109).
func TestUnexpectedErrorsLogNoPaths(t *testing.T) {
	secret := "incident-" + strings.Repeat("q7", 4) + ".md"
	leaky := &iofs.PathError{Op: "open", Path: "notes/" + secret,
		Err: errors.New("cannot open " + secret + "\n" + `{"level":"INFO","msg":"forged"}`)}
	for _, tc := range []struct {
		name string
		log  func(*slog.Logger)
	}{
		{"notes API", func(l *slog.Logger) {
			a := &NotesAPI{log: l}
			a.fail(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/notes/x", nil), leaky)
		}},
		{"auth API", func(l *slog.Logger) {
			a := &AuthAPI{log: l}
			a.fail(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/login", nil), leaky)
		}},
		{"initial import", func(l *slog.Logger) {
			f := importFails{err: leaky}
			a := &AuthAPI{auth: f, importer: f, log: l}
			r := httptest.NewRequest("POST", "/api/v1/setup", strings.NewReader(`{"token":"t","password":"p"}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			a.setup(w, r)
			if w.Code != stdhttp.StatusCreated {
				t.Fatalf("setup = %d %s, want 201: a failed import doesn't fail setup", w.Code, w.Body)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			tc.log(slog.New(slog.NewJSONHandler(&buf, nil)))
			line := buf.String()
			if strings.Contains(line, secret) || strings.Contains(line, "forged") || strings.Count(line, "\n") != 1 {
				t.Fatalf("the log leaks user text: %s", line)
			}
			if !strings.Contains(line, `"err":{"op":"open","errno":"none","kind":"unexpected"}`) {
				t.Fatalf("logged %s, want the pathless description", line)
			}
		})
	}
}

// TestPathlessNamesTheErrno: a system error inside a PathError is named, so the log still
// tells an operator what failed.
func TestPathlessNamesTheErrno(t *testing.T) {
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Error("x", pathless(&iofs.PathError{Op: "write", Path: "a.md", Err: syscall.ENOSPC}))
	if !strings.Contains(buf.String(), `"err":{"op":"write","errno":"ENOSPC","kind":"unexpected"}`) {
		t.Fatalf("logged %s", buf.String())
	}
}
