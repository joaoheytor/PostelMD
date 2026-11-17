//go:build linux

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/config"
	"github.com/joaoheytor/PostelMD/internal/ids"
	"github.com/joaoheytor/PostelMD/internal/store/layout"
	sqlstore "github.com/joaoheytor/PostelMD/internal/store/sql"
)

// errDeclined: vault claim's confirmation didn't match. Nothing was changed.
var errDeclined = errors.New("the answer didn't match, so nothing was changed")

// isTerminal tells whether vault claim may ask; the tests answer through a pipe.
var isTerminal = term.IsTerminal

// vaultClaim writes the vault marker with this database's vault UUID (ADR-0030, ADR-0040),
// with the server stopped, after showing the identity check and asking.
func vaultClaim(cfg config.Config, stdin *os.File, stdout io.Writer) error {
	return offline(cfg, func(paths layout.Paths) error {
		ctx := context.Background()
		if err := sqlstore.UpgradeUnderWay(paths.State); err != nil {
			return err
		}
		db, err := sqlstore.OpenExisting(ctx, paths.State, slog.New(slog.DiscardHandler))
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		c, err := ids.CheckClaim(ctx, db, paths.Vault)
		if err != nil {
			return err
		}
		if c.Done {
			_, err := fmt.Fprintln(stdout, "The vault marker already names this database's vault: nothing to do.")
			return err
		}
		_, _ = fmt.Fprint(stdout, claimSummary(paths.Vault, c))
		typed, err := confirmClaim(cfg, stdin, stdout, paths.Vault, c.Passes())
		if err != nil {
			return err
		}
		if err := ids.Claim(paths.Vault, c, layout.FormatVersion); err != nil {
			return err
		}
		// The audit record goes to stdout, the sink serve uses (ADR-0020), as reset-password's.
		audit.New(slog.New(slog.NewJSONHandler(stdout, nil))).Record(ctx, "vault.claimed",
			slog.String("vault_uuid", c.VaultUUID), slog.String("replaced_uuid", c.MarkerUUID),
			slog.Bool("check_passed", c.Passes()), slog.Bool("force", cfg.Force), slog.Bool("yes", cfg.Yes),
			slog.Bool("typed_path", typed))
		_, err = fmt.Fprintln(stdout, "Claimed: the vault marker names this database's vault. Start the server now.")
		return err
	})
}

// claimSummary is what vault claim shows before it asks.
func claimSummary(vaultDir string, c ids.ClaimCheck) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Vault: %s\nFiles in the vault: %d\n", vaultDir, c.Files)
	if c.MarkerUUID != "" {
		fmt.Fprintf(&b, "The vault marker names another vault: %s (this database's is %s)\n", c.MarkerUUID, c.VaultUUID)
	}
	fmt.Fprintf(&b, "File IDs checked: %d, found at their path with the same inode and size: %d\n", c.Sampled, c.Matched)
	if c.Passes() {
		b.WriteString("The identity check passes: this looks like the vault the database describes.\n")
	} else {
		b.WriteString("The identity check FAILS: after a claim, the next start tombstones every file ID " +
			"whose file it doesn't find, and those files get new IDs.\n")
	}
	return b.String()
}

// confirmClaim asks, or reads the flags: --force always claims, --yes only when the check
// passes. On a terminal, the answer is "yes" or the vault's path when it passes, else only
// the vault's path. It reports whether the vault's path was typed.
func confirmClaim(cfg config.Config, stdin *os.File, stdout io.Writer, vaultDir string, passes bool) (typed bool, err error) {
	switch {
	case cfg.Force, cfg.Yes && passes:
		return false, nil
	case cfg.Yes:
		return false, errors.New("the identity check fails, so --yes isn't enough and nothing was changed: " +
			"add --force, or run this on a terminal and type the vault's path")
	}
	fd := int(stdin.Fd()) //nolint:gosec // G115: a file descriptor fits in int
	if !isTerminal(fd) {
		flag := "--yes"
		if !passes {
			flag = "--force"
		}
		return false, fmt.Errorf("nothing was changed: there is no terminal to ask on; add %s to claim", flag)
	}
	if passes {
		_, _ = fmt.Fprint(stdout, "Type yes (or the vault's path) to claim it: ")
	} else {
		_, _ = fmt.Fprintf(stdout, "Type the vault's path (%s) to claim it anyway: ", vaultDir)
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.TrimRight(line, "\r\n") {
	case vaultDir:
		return true, nil
	case "yes":
		if passes {
			return false, nil
		}
	}
	return false, errDeclined
}
