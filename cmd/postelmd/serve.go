package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/joaoheytor/PostelMD/internal/auth"
	"github.com/joaoheytor/PostelMD/internal/config"
	"github.com/joaoheytor/PostelMD/internal/egress"
	pmhttp "github.com/joaoheytor/PostelMD/internal/http"
	"github.com/joaoheytor/PostelMD/internal/store/git"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	"github.com/joaoheytor/PostelMD/internal/store/lock"
	"github.com/joaoheytor/PostelMD/internal/vault"
	"github.com/joaoheytor/PostelMD/web"
)

// shutdownDeadline bounds a graceful stop (architecture §8; Docker's --stop-timeout is 60).
const shutdownDeadline = 45 * time.Second

// layoutOptions are the operator's overrides of the data directory checks. opensDatabase is
// true for a command that opens app.db (ADR-0030: the network override can't cover it).
func layoutOptions(cfg config.Config, opensDatabase bool) layout.Options {
	return layout.Options{AllowNetworkFS: cfg.AllowNetworkFS, AllowEphemeralData: cfg.AllowEphemeralData,
		OpensDatabase: opensDatabase}
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

// afterListen, when a test sets it, runs once the listener serves and before the format
// check, with the listening address.
var afterListen func(addr string)

// serve runs the server until ctx ends. The startup order (ADR-0030, as far as this build
// goes) is: layout and the filesystem checks of the state and vault directories → instance
// lock → the HTTP listener, which answers /healthz and 503 elsewhere (pmhttp.Starting) →
// openInstance (format, app.db, auth, vault) → every route. ready, if set, gets the
// listening address once every route is served.
func serve(ctx context.Context, cfg config.Config, stdout io.Writer, ready func(addr string)) error {
	log := slog.New(slog.NewJSONHandler(stdout, nil))
	paths, durability, err := layout.Prepare(cfg.DataDir, layoutOptions(cfg, true))
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

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	gate := &pmhttp.Starting{}
	srv := newHTTPServer(cfg, gate, log)
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(ln) }()
	addr := ln.Addr().String()
	log.Info("listening", "addr", addr, "vault", paths.Vault, "version", version, "commit", commit)
	if afterListen != nil {
		afterListen(addr)
	}

	in, err := openInstance(ctx, paths, lk.BootUUID(), log)
	if err == nil {
		if err = announceSetup(in.auth, cfg.Listen, addr, stdout); err != nil {
			in.close(log, true)
		}
	}
	if err != nil {
		_ = closeNow(srv, errs)
		return err
	}
	gate.Ready(pmhttp.NewRouter(in.auth, in.vault, web.Dist(), pmhttp.Build{Version: version, Commit: commit}, log))
	log.Info("ready")
	if ready != nil {
		ready(addr)
	}
	return serveUntilStopped(ctx, srv, errs, in, lk.BootUUID(), log)
}

// serveUntilStopped serves until ctx ends, the server fails, or the boot UUID check finds a second writer.
// A normal stop is graceful (shutdownDeadline); a split brain closes every connection at
// once, so no request in flight writes on, and leaves the database unclosed.
func serveUntilStopped(ctx context.Context, srv *http.Server, errs chan error, in *instance, boot string, log *slog.Logger) error {
	watch, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	split := make(chan error, 1)
	go func() { split <- watchBoot(watch, in.db, boot, log) }()

	select {
	case err := <-errs:
		in.close(log, true)
		return err
	case err := <-split:
		if err != nil {
			_ = closeNow(srv, errs)
			in.close(log, false)
			return err
		}
	case <-ctx.Done():
	}
	log.Info("shutting down")
	stop, cancel := context.WithTimeout(context.Background(), shutdownDeadline)
	defer cancel()
	err := srv.Shutdown(stop)
	if serr := <-errs; err == nil && !errors.Is(serr, http.ErrServerClosed) {
		err = serr
	}
	in.close(log, true)
	return err
}

// closeNow closes the listener and every connection at once, and waits for Serve to return.
func closeNow(srv *http.Server, errs chan error) error {
	err := srv.Close()
	if serr := <-errs; !errors.Is(serr, http.ErrServerClosed) {
		err = errors.Join(err, serr)
	}
	return err
}

// installGitTransports leaves go-git only the https transport, through the egress client,
// before any vault is opened (threat model I13, #40). go-git's transports are process-wide,
// so it runs once per process, however many times serve runs (tests). After any serve, a
// test binary's go-git is https-only too.
var installGitTransports = sync.OnceFunc(func() {
	git.InstallTransports(egress.New(egress.Options{}))
})

// openVault opens the vault at dir and its git repository (adopted, or created empty).
func openVault(dir string, log *slog.Logger) (*vault.Vault, error) {
	v, err := vault.Open(dir)
	if err != nil {
		return nil, err
	}
	v.SetLogger(log)
	created, err := v.EnableGit()
	if err != nil {
		_ = v.Close()
		return nil, err
	}
	if created {
		log.Info("created a git repository for the vault", "vault", dir)
	}
	return v, nil
}

// announceSetup prints the setup page's address and a fresh one-time token when no password
// is set yet. Printed, never logged as a record: the one place a token reaches stdout
// (ADR-0009). The token stays out of the URL, so it isn't kept in the browser's history.
func announceSetup(svc *auth.Service, listen, actual string, stdout io.Writer) error {
	if !svc.NeedsSetup() {
		return nil
	}
	tok, err := svc.NewSetupToken()
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "\nPostelMD isn't set up yet. Open %s in your browser (with docker -p, use\n"+
		"the host port you published) and use this one-time token\n(valid %s): %s\n\n",
		setupURL(listen, actual), auth.SetupTokenTTL, tok)
	return nil
}

// newHTTPServer is the HTTP server with its timeouts and limits, and the host check in front
// of h (the starting gate, then the router).
func newHTTPServer(cfg config.Config, h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           pmhttp.AllowHosts(cfg.AllowedHosts, log, h),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}
