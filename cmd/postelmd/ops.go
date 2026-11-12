package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/auth"
	"github.com/joaoheytor/PostelMD/internal/config"
	"github.com/joaoheytor/PostelMD/internal/egress"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	"github.com/joaoheytor/PostelMD/internal/store/lock"
)

// resetPassword sets a new password with the server stopped: it takes the instance lock,
// so it refuses to run next to a live server. On a terminal the password is read twice
// without echo; otherwise one line is read from stdin.
func resetPassword(cfg config.Config, stdin *os.File, stdout io.Writer) error {
	paths, _, err := layout.Prepare(cfg.DataDir, layoutOptions(cfg))
	if err != nil {
		return err
	}
	lk, err := lock.Acquire(paths.Lock)
	if err != nil {
		return fmt.Errorf("%w; stop the server first", err)
	}
	defer func() { _ = lk.Release() }()
	if err := layout.EnsureFormat(paths.State); err != nil { // never change a newer build's data
		return err
	}
	pw, err := readPassword(stdin, stdout)
	if err != nil {
		return err
	}
	// The audit record goes to stdout, the sink serve uses (ADR-0020).
	a := audit.New(slog.New(slog.NewJSONHandler(stdout, nil)))
	if err := auth.ResetPassword(context.Background(), paths.State, pw, a); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "Password changed. Every session has been logged out.")
	return nil
}

func readPassword(stdin *os.File, stdout io.Writer) (string, error) {
	fd := int(stdin.Fd()) //nolint:gosec // G115: a file descriptor fits in int
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	_, _ = fmt.Fprint(stdout, "New password: ")
	first, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(stdout)
	if err != nil {
		return "", err
	}
	_, _ = fmt.Fprint(stdout, "Repeat it: ")
	second, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(stdout)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("the passwords don't match")
	}
	return string(first), nil
}

// healthcheck asks the local server's /healthz, for the image's HEALTHCHECK (the image has
// no shell or curl). It goes through the egress client (rule 13), with an exception for
// exactly this loopback address.
func healthcheck(cfg config.Config) error {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "" || ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, port)
	c := egress.New(egress.Options{Exceptions: []string{addr}, AllowHTTP: true, Timeout: 3 * time.Second})
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("healthz answered %s", resp.Status)
	}
	return nil
}
