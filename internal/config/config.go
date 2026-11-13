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
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config is the server's configuration.
type Config struct {
	DataDir            string // POSTELMD_DATA_DIR, --data; default /data; made absolute. Not in config.yaml: it holds the file
	Listen             string // listen, POSTELMD_LISTEN, --listen; default :8080
	AllowNetworkFS     bool   // allow_network_fs, POSTELMD_ALLOW_NETWORK_FS; unsafe (ADR-0014)
	AllowEphemeralData bool   // allow_ephemeral_data, POSTELMD_ALLOW_EPHEMERAL_DATA: start with no volume
	// AllowedHosts: allowed_hosts, POSTELMD_ALLOWED_HOSTS (comma-separated). The host names,
	// besides localhost and IP literals, that requests may name (DNS rebinding; ADR-0005).
	AllowedHosts []string
}

// file is config.yaml. Pointers tell a setting that's absent from one set to its zero value.
type file struct {
	Listen             *string  `yaml:"listen"`
	AllowNetworkFS     *bool    `yaml:"allow_network_fs"`
	AllowEphemeralData *bool    `yaml:"allow_ephemeral_data"`
	AllowedHosts       []string `yaml:"allowed_hosts"`
}

// ErrPrinted wraps a flag parsing error (flag.ErrHelp included) that the flag package has
// already written to stderr, with the usage: the caller doesn't print it again.
var ErrPrinted = errors.New("config: the flag package printed this error")

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
		return c, fmt.Errorf("%w: %w", ErrPrinted, err)
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
	// Absolute and clean, resolved once here: store/sql refuses relative database paths, and
	// every later step and command must name the same directory (ADR-0030, follow-up 2). An
	// empty one is refused: filepath.Abs would make it the working directory.
	if c.DataDir == "" {
		return c, errors.New("config: the data directory (--data) is empty")
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return c, fmt.Errorf("config: the data directory: %w", err)
	}
	c.DataDir = abs

	if err := c.readFile(); err != nil {
		return c, err
	}

	if v := getenv("POSTELMD_LISTEN"); v != "" {
		c.Listen = v
	}
	for name, dst := range map[string]*bool{
		"POSTELMD_ALLOW_NETWORK_FS":     &c.AllowNetworkFS,
		"POSTELMD_ALLOW_EPHEMERAL_DATA": &c.AllowEphemeralData,
	} {
		if v := getenv(name); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return c, fmt.Errorf("config: %s=%q isn't a boolean", name, v)
			}
			*dst = b
		}
	}
	if v := getenv("POSTELMD_ALLOWED_HOSTS"); v != "" {
		hosts, err := hostNames(strings.Split(v, ","))
		if err != nil {
			return c, fmt.Errorf("config: POSTELMD_ALLOWED_HOSTS: %w", err)
		}
		c.AllowedHosts = hosts
	}
	if set["listen"] {
		c.Listen = flagListen
	}
	return c, nil
}

// hostNames normalizes allowed host names (trimmed, lower case, no trailing dot) and refuses
// anything that isn't one plain name: a wildcard, a port, a scheme or a path would either
// not match or open the allowlist wider than its author meant.
func hostNames(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, h := range in {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h == "" || len(h) > 253 || strings.Trim(h, "abcdefghijklmnopqrstuvwxyz0123456789-.") != "" ||
			strings.HasPrefix(h, ".") || strings.Contains(h, "..") {
			return nil, fmt.Errorf("%q isn't a host name (no port, scheme, path or wildcard)", h)
		}
		out = append(out, h)
	}
	return out, nil
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
	if f.AllowEphemeralData != nil {
		c.AllowEphemeralData = *f.AllowEphemeralData
	}
	if f.AllowedHosts != nil {
		hosts, err := hostNames(f.AllowedHosts)
		if err != nil {
			return fmt.Errorf("config: %s: allowed_hosts: %w", path, err)
		}
		c.AllowedHosts = hosts
	}
	return nil
}
