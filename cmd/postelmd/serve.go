package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/auth"
	"github.com/joaoheytor/PostelMD/internal/config"
	pmhttp "github.com/joaoheytor/PostelMD/internal/http"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	"github.com/joaoheytor/PostelMD/internal/store/lock"
	"github.com/joaoheytor/PostelMD/internal/vault"
	"github.com/joaoheytor/PostelMD/web"
)

// shutdownDeadline bounds a graceful stop (architecture §8; Docker's --stop-timeout is 60).
const shutdownDeadline = 45 * time.Second

// layoutOptions are the operator's overrides of the data directory checks.
func layoutOptions(cfg config.Config) layout.Options {
	return layout.Options{AllowNetworkFS: cfg.AllowNetworkFS, AllowEphemeralData: cfg.AllowEphemeralData}
}

// setupURL is the setup page's address for the startup message: the listen address's host,
// or localhost when it listens on every interface, and the port it actually got.
func setupURL(listen, actual string) string {
	host, _, _ := net.SplitHostPort(listen)
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "localhost"
	}
	_, port, _ := net.SplitHostPort(actual)
	return "http://" + net.JoinHostPort(host, port) + "/setup"
}

// serve runs the server until ctx ends. The startup order is: layout and the filesystem
// checks of the state and vault directories → instance lock → auth state → vault (temp
// sweep, rename probe) → the vault's git repository (adopted, or created empty) → HTTP.
// ready, if set, gets the listening address.
func serve(ctx context.Context, cfg config.Config, stdout io.Writer, ready func(addr string)) error {
	log := slog.New(slog.NewJSONHandler(stdout, nil))
	paths, durability, err := layout.Prepare(cfg.DataDir, layoutOptions(cfg))
	if err != nil {
		return err
	}
	if durability != layout.Persistent {
		log.Warn("the data directory is not persistent: notes are lost when the container is removed; mount a volume at /data")
	}
	lk, err := lock.Acquire(paths.Lock)
	if err != nil {
		return err
	}
	defer func() { _ = lk.Release() }()

	svc, err := auth.OpenDir(paths.State, audit.New(log))
	if err != nil {
		return err
	}
	defer func() { _ = svc.Close() }()
	v, err := vault.Open(paths.Vault)
	if err != nil {
		return err
	}
	defer func() { _ = v.Close() }()
	v.SetLogger(log)
	created, err := v.EnableGit()
	if err != nil {
		return err
	}
	if created {
		log.Info("created a git repository for the vault", "vault", paths.Vault)
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	if svc.NeedsSetup() {
		tok, err := svc.NewSetupToken()
		if err != nil {
			return err
		}
		// Printed, never logged as a record: the one place a token reaches stdout (ADR-0009).
		// The token stays out of the URL, so it isn't kept in the browser's history.
		_, _ = fmt.Fprintf(stdout, "\nPostelMD isn't set up yet. Open %s in your browser (with docker -p, use\n"+
			"the host port you published) and use this one-time token\n(valid %s): %s\n\n",
			setupURL(cfg.Listen, ln.Addr().String()), auth.SetupTokenTTL, tok)
	}
	srv := &http.Server{
		Handler:           pmhttp.AllowHosts(cfg.AllowedHosts, log, pmhttp.NewRouter(svc, v, web.Dist(), log)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(ln) }()
	log.Info("listening", "addr", ln.Addr().String(), "vault", paths.Vault)
	if ready != nil {
		ready(ln.Addr().String())
	}

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	stop, cancel := context.WithTimeout(context.Background(), shutdownDeadline)
	defer cancel()
	if err := srv.Shutdown(stop); err != nil {
		return err
	}
	if err := <-errs; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
