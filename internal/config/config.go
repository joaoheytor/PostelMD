// Package config is the only place settings are read (CLAUDE.md rule 7). Precedence:
// flags > environment > defaults. The MVP has three settings; config.yaml and _FILE secrets
// arrive with the first setting that needs them.
package config

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
)

// Config is the server's configuration.
type Config struct {
	DataDir        string // POSTELMD_DATA_DIR, --data; default /data
	Listen         string // POSTELMD_LISTEN, --listen; default :8080
	AllowNetworkFS bool   // POSTELMD_ALLOW_NETWORK_FS; unsafe (ADR-0014)
}

// Load reads flags from args, then the environment.
func Load(name string, args []string, stderr io.Writer) (Config, error) {
	return load(name, args, os.Getenv, stderr)
}

func load(name string, args []string, getenv func(string) string, stderr io.Writer) (Config, error) {
	c := Config{DataDir: "/data", Listen: ":8080"}
	if v := getenv("POSTELMD_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := getenv("POSTELMD_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := getenv("POSTELMD_ALLOW_NETWORK_FS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("config: POSTELMD_ALLOW_NETWORK_FS=%q isn't a boolean", v)
		}
		c.AllowNetworkFS = b
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.DataDir, "data", c.DataDir, "data directory (POSTELMD_DATA_DIR)")
	fs.StringVar(&c.Listen, "listen", c.Listen, "listen address (POSTELMD_LISTEN)")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("config: unexpected argument %q", fs.Arg(0))
	}
	return c, nil
}
