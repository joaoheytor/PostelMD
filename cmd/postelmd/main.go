// Command postelmd is the PostelMD server and its operational subcommands.
//
// Only `version` exists so far; `serve` and the other subcommands listed in CLAUDE.md
// arrive with the milestones in docs/roadmap.md.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "none"
)

const usage = `Usage: postelmd <command>

Commands:
  version   Print the version and build information
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
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "postelmd %s (commit %s, %s, %s/%s)\n",
			version, commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "postelmd: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
