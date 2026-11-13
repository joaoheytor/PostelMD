package main

import (
	"bytes"
	"strings"
	"testing"
)

// runAndCheck runs args and checks the exit code, and that stdout and stderr contain the
// wanted text (no stdout at all when none is wanted). It returns stderr.
func runAndCheck(t *testing.T, args []string, wantCode int, wantStdout, wantStderr string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	if code != wantCode {
		t.Errorf("exit code = %d, want %d", code, wantCode)
	}
	if !strings.Contains(stdout.String(), wantStdout) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), wantStdout)
	}
	if !strings.Contains(stderr.String(), wantStderr) {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), wantStderr)
	}
	if wantStdout == "" && stdout.Len() != 0 {
		t.Errorf("unexpected stdout: %q", stdout.String())
	}
	return stderr.String()
}

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "no args prints usage", args: nil, wantCode: 2, wantStderr: "Usage: postelmd"},
		{name: "version", args: []string{"version"}, wantCode: 0, wantStdout: "postelmd dev (commit none,"},
		{name: "version flag", args: []string{"--version"}, wantCode: 0, wantStdout: "postelmd dev"},
		{name: "help", args: []string{"help"}, wantCode: 0, wantStdout: "Commands:"},
		{name: "unknown command", args: []string{"serv"}, wantCode: 2, wantStderr: `unknown command "serv"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runAndCheck(t, tt.args, tt.wantCode, tt.wantStdout, tt.wantStderr)
		})
	}
}

// TestRunReportsConfigErrors: a configuration error exits 2 and says why on stderr, once.
// The flag package prints its own parse errors and the help text; those aren't repeated.
func TestRunReportsConfigErrors(t *testing.T) {
	missing := t.TempDir() + "/missing" // no config.yaml to read
	for _, tt := range []struct {
		name, env  string
		args       []string
		wantStderr string
	}{
		{"an empty --data", "", []string{"serve", "--data", ""},
			"postelmd serve: config: the data directory (--data) is empty\n"},
		{"an empty --data=", "", []string{"reset-password", "--data="},
			"postelmd reset-password: config: the data directory (--data) is empty\n"},
		{"a bad boolean in the environment", "maybe", []string{"serve", "--data", missing},
			`postelmd serve: config: POSTELMD_ALLOW_NETWORK_FS="maybe" isn't a boolean` + "\n"},
		{"an unknown flag, printed by the flag package", "", []string{"serve", "--bogus"},
			"flag provided but not defined: -bogus\n"},
		{"help, printed by the flag package", "", []string{"serve", "-h"}, "Usage of serve:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("POSTELMD_ALLOW_NETWORK_FS", tt.env)
			}
			stderr := runAndCheck(t, tt.args, 2, "", tt.wantStderr)
			if strings.Count(stderr, tt.wantStderr) != 1 || strings.Contains(stderr, "the flag package printed") {
				t.Errorf("stderr = %q, want %q exactly once and no repeat of the flag package's error", stderr, tt.wantStderr)
			}
		})
	}
}
