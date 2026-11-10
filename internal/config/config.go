// Package config is the only place settings are read (CLAUDE.md rule 7). Precedence:
// flags > environment > /data/.postelmd/config.yaml > defaults (ADR-0009). _FILE secrets
// arrive with the first secret setting.
package config

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// Config is the server's configuration.
type Config struct {
	DataDir        string // POSTELMD_DATA_DIR, --data; default /data. Not in config.yaml: it holds the file
	Listen         string // listen, POSTELMD_LISTEN, --listen; default :8080
	AllowNetworkFS bool   // allow_network_fs, POSTELMD_ALLOW_NETWORK_FS; unsafe (ADR-0014)
}

// file is config.yaml. Pointers tell a setting that's absent from one set to its zero value.
type file struct {
	Listen         *string `yaml:"listen"`
	AllowNetworkFS *bool   `yaml:"allow_network_fs"`
}

// Load reads flags from args, the environment, then the data directory's config.yaml.
func Load(name string, args []string, stderr io.Writer) (Config, error) {
	return load(name, args, os.Getenv, stderr)
}

func load(name string, args []string, getenv func(string) string, stderr io.Writer) (Config, error) {
	c := Config{DataDir: "/data", Listen: ":8080"}

	// Flags first: --data says where config.yaml is. They're applied last.
	var flagData, flagListen string
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(stderr)
	fl.StringVar(&flagData, "data", c.DataDir, "data directory (POSTELMD_DATA_DIR)")
	fl.StringVar(&flagListen, "listen", c.Listen, "listen address (POSTELMD_LISTEN, config.yaml listen)")
	if err := fl.Parse(args); err != nil {
		return c, err
	}
	if fl.NArg() > 0 {
		return c, fmt.Errorf("config: unexpected argument %q", fl.Arg(0))
	}
	set := map[string]bool{}
	fl.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if v := getenv("POSTELMD_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if set["data"] {
		c.DataDir = flagData
	}

	if err := c.readFile(); err != nil {
		return c, err
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
	if set["listen"] {
		c.Listen = flagListen
	}
	return c, nil
}

// readFile applies the data directory's config.yaml, if there is one. It is strict: an
// unknown key (a typo, or data_dir) is an error, not a silently ignored setting. This is the
// one read outside internal/store (CLAUDE.md rule 1), at bootstrap, before the instance lock.
func (c *Config) readFile() error {
	path := filepath.Join(c.DataDir, ".postelmd", "config.yaml")
	b, err := os.ReadFile(path) //nolint:gosec // G304: the operator's own data directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil // zero config (ADR-0009)
	}
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var f file
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("config: %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("config: %s must hold one document", path)
	}
	if f.Listen != nil {
		c.Listen = *f.Listen
	}
	if f.AllowNetworkFS != nil {
		c.AllowNetworkFS = *f.AllowNetworkFS
	}
	return nil
}
