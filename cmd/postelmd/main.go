// Command postelmd is the PostelMD server and its operational subcommands.
//
// The MVP has serve, reset-password, healthcheck and version (ADR-0020); v0.2 adds db
// rollback (ADR-0035); the other
// subcommands listed in CLAUDE.md arrive with the milestones in docs/roadmap.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/joaoheytor/PostelMD/internal/config"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "none"
)

const usage = `Usage: postelmd <command>

Commands:
  serve            Run the server (flags: --data, --listen)
  reset-password   Set a new password, with the server stopped
  db rollback      Undo a database migration that didn't finish, with the server stopped
  healthcheck      Exit 0 if the local server answers /healthz
  version          Print the version and build information
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes a subcommand and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	name, rest := args[0], args[1:]
	if name == "db" {
		if len(rest) == 0 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		name, rest = "db "+rest[0], rest[1:]
	}
	switch name {
	case "serve", "reset-password", "healthcheck", "db rollback":
		cfg, err := config.Load(name, rest, stderr)
		if err != nil {
			if !errors.Is(err, config.ErrPrinted) {
				fmt.Fprintf(stderr, "postelmd %s: %v\n", name, err)
			}
			return 2
		}
		return report(stderr, name, runCommand(name, cfg, stdout))
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "postelmd %s (commit %s, %s, %s/%s)\n",
			version, commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "postelmd: unknown command %q\n\n%s", name, usage)
		return 2
	}
}

func runCommand(name string, cfg config.Config, stdout io.Writer) error {
	switch name {
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return serve(ctx, cfg, stdout, nil)
	case "reset-password":
		return resetPassword(cfg, os.Stdin, stdout)
	case "db rollback":
		return dbRollback(cfg, stdout)
	default:
		return healthcheck(cfg)
	}
}

func report(stderr io.Writer, name string, err error) int {
	if err != nil {
		fmt.Fprintf(stderr, "postelmd %s: %v\n", name, err)
		return 1
	}
	return 0
}
