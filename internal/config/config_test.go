package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrecedence(t *testing.T) {
	env := map[string]string{"POSTELMD_DATA_DIR": "/env", "POSTELMD_LISTEN": ":9000", "POSTELMD_ALLOW_NETWORK_FS": "true"}
	get := func(k string) string { return env[k] }
	c, err := load("serve", nil, func(string) string { return "" }, io.Discard)
	if err != nil || c != (Config{DataDir: "/data", Listen: ":8080"}) {
		t.Fatalf("defaults = %+v, %v", c, err)
	}
	c, err = load("serve", nil, get, io.Discard)
	if err != nil || c != (Config{DataDir: "/env", Listen: ":9000", AllowNetworkFS: true}) {
		t.Fatalf("env = %+v, %v", c, err)
	}
	c, err = load("serve", []string{"--data", "/flag", "--listen", "127.0.0.1:1"}, get, io.Discard)
	if err != nil || c.DataDir != "/flag" || c.Listen != "127.0.0.1:1" {
		t.Fatalf("flags = %+v, %v", c, err)
	}
	env["POSTELMD_ALLOW_NETWORK_FS"] = "yes please"
	if _, err := load("serve", nil, get, io.Discard); err == nil {
		t.Fatal("a non-boolean override was accepted")
	}
	if _, err := load("serve", []string{"extra"}, get, io.Discard); err == nil {
		t.Fatal("a stray argument was accepted")
	}
}

// dataDir makes a data directory holding .postelmd/config.yaml with body ("" for no file).
func dataDir(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if body == "" {
		return dir
	}
	if err := os.MkdirAll(filepath.Join(dir, ".postelmd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".postelmd", "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestConfigFile(t *testing.T) {
	const file = "listen: \"127.0.0.1:7000\"\nallow_network_fs: true\n"
	noEnv := func(string) string { return "" }

	dir := dataDir(t, file)
	c, err := load("serve", []string{"--data", dir}, noEnv, io.Discard)
	if err != nil || c != (Config{DataDir: dir, Listen: "127.0.0.1:7000", AllowNetworkFS: true}) {
		t.Fatalf("file only = %+v, %v", c, err)
	}

	// The environment beats the file, and flags beat both.
	env := map[string]string{"POSTELMD_DATA_DIR": dir, "POSTELMD_LISTEN": ":9000", "POSTELMD_ALLOW_NETWORK_FS": "false"}
	get := func(k string) string { return env[k] }
	c, err = load("serve", nil, get, io.Discard)
	if err != nil || c.Listen != ":9000" || c.AllowNetworkFS {
		t.Fatalf("env over file = %+v, %v", c, err)
	}
	c, err = load("serve", []string{"--listen", "127.0.0.1:1"}, get, io.Discard)
	if err != nil || c.Listen != "127.0.0.1:1" {
		t.Fatalf("flag over env and file = %+v, %v", c, err)
	}

	// No file is the zero-config case (ADR-0009).
	empty := dataDir(t, "")
	c, err = load("serve", []string{"--data", empty}, noEnv, io.Discard)
	if err != nil || c != (Config{DataDir: empty, Listen: ":8080"}) {
		t.Fatalf("no file = %+v, %v", c, err)
	}
}

func TestConfigFileRefusesWhatItCantUse(t *testing.T) {
	noEnv := func(string) string { return "" }
	for name, tc := range map[string]struct{ body, want string }{
		"a typo":             {"lisen: \":1\"\n", "lisen"},
		"the data directory": {"data_dir: /elsewhere\n", "data_dir"},
		"malformed YAML":     {"listen: [\n", "config.yaml"},
		"a non-boolean":      {"allow_network_fs: maybe\n", "line 1"},
		"two documents":      {"listen: \":1\"\n---\nlisten: \":2\"\n", "one document"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := dataDir(t, tc.body)
			_, err := load("serve", []string{"--data", dir}, noEnv, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
}
