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

	"github.com/joaoheytor/PostelMD/internal/audit"
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
// checks of the state and vault directories → instance lock → data format (ADR-0025) → auth
// state → go-git transports (https only, through egress) → vault (adoption check, temp
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
	if err := layout.EnsureFormat(paths.State); err != nil {
		return err
	}

	svc, err := auth.OpenDir(paths.State, audit.New(log))
	if err != nil {
		return err
	}
	defer func() { _ = svc.Close() }()
	installGitTransports()
	v, err := openVault(paths.Vault, log)
	if err != nil {
		return err
	}
	defer func() { _ = v.Close() }()

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	if err := announceSetup(svc, cfg.Listen, ln.Addr().String(), stdout); err != nil {
		return err
	}
	srv := newHTTPServer(cfg, svc, v, log)
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(ln) }()
	log.Info("listening", "addr", ln.Addr().String(), "vault", paths.Vault, "version", version, "commit", commit)
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
// of the router.
func newHTTPServer(cfg config.Config, svc *auth.Service, v *vault.Vault, log *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           pmhttp.AllowHosts(cfg.AllowedHosts, log, pmhttp.NewRouter(svc, v, web.Dist(), pmhttp.Build{Version: version, Commit: commit}, log)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}
