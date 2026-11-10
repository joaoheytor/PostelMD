package config

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrecedence(t *testing.T) {
	env := map[string]string{"POSTELMD_DATA_DIR": "/env", "POSTELMD_LISTEN": ":9000", "POSTELMD_ALLOW_NETWORK_FS": "true",
		"POSTELMD_ALLOW_EPHEMERAL_DATA": "1", "POSTELMD_ALLOWED_HOSTS": " Notes.Example.org., wiki.internal "}
	get := func(k string) string { return env[k] }
	c, err := load("serve", nil, func(string) string { return "" }, io.Discard)
	if err != nil || !reflect.DeepEqual(c, Config{DataDir: "/data", Listen: ":8080"}) {
		t.Fatalf("defaults = %+v, %v", c, err)
	}
	c, err = load("serve", nil, get, io.Discard)
	if err != nil || !reflect.DeepEqual(c, Config{DataDir: "/env", Listen: ":9000", AllowNetworkFS: true, AllowEphemeralData: true,
		AllowedHosts: []string{"notes.example.org", "wiki.internal"}}) {
		t.Fatalf("env = %+v, %v", c, err)
	}
	c, err = load("serve", []string{"--data", "/flag", "--listen", "127.0.0.1:1"}, get, io.Discard)
	if err != nil || c.DataDir != "/flag" || c.Listen != "127.0.0.1:1" {
		t.Fatalf("flags = %+v, %v", c, err)
	}
	for _, name := range []string{"POSTELMD_ALLOW_NETWORK_FS", "POSTELMD_ALLOW_EPHEMERAL_DATA"} {
		old := env[name]
		env[name] = "yes please"
		if _, err := load("serve", nil, get, io.Discard); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("a non-boolean %s = %v, want an error naming it", name, err)
		}
		env[name] = old
	}
	env["POSTELMD_ALLOWED_HOSTS"] = "notes.example.org,*.example.org"
	if _, err := load("serve", nil, get, io.Discard); err == nil || !strings.Contains(err.Error(), "POSTELMD_ALLOWED_HOSTS") {
		t.Fatalf("a wildcard in POSTELMD_ALLOWED_HOSTS = %v, want an error naming it", err)
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
	const file = "listen: \"127.0.0.1:7000\"\nallow_network_fs: true\nallow_ephemeral_data: true\nallowed_hosts: [notes.example.org]\n"
	noEnv := func(string) string { return "" }

	dir := dataDir(t, file)
	c, err := load("serve", []string{"--data", dir}, noEnv, io.Discard)
	if err != nil || !reflect.DeepEqual(c, Config{DataDir: dir, Listen: "127.0.0.1:7000", AllowNetworkFS: true, AllowEphemeralData: true,
		AllowedHosts: []string{"notes.example.org"}}) {
		t.Fatalf("file only = %+v, %v", c, err)
	}

	// The environment beats the file, and flags beat both.
	env := map[string]string{"POSTELMD_DATA_DIR": dir, "POSTELMD_LISTEN": ":9000", "POSTELMD_ALLOW_NETWORK_FS": "false",
		"POSTELMD_ALLOW_EPHEMERAL_DATA": "false"}
	get := func(k string) string { return env[k] }
	c, err = load("serve", nil, get, io.Discard)
	if err != nil || c.Listen != ":9000" || c.AllowNetworkFS || c.AllowEphemeralData || len(c.AllowedHosts) != 1 {
		t.Fatalf("env over file = %+v, %v", c, err)
	}
	env["POSTELMD_ALLOWED_HOSTS"] = "wiki.internal"
	c, err = load("serve", nil, get, io.Discard)
	if err != nil || !reflect.DeepEqual(c.AllowedHosts, []string{"wiki.internal"}) {
		t.Fatalf("env over file = %+v, %v", c, err)
	}
	c, err = load("serve", []string{"--listen", "127.0.0.1:1"}, get, io.Discard)
	if err != nil || c.Listen != "127.0.0.1:1" {
		t.Fatalf("flag over env and file = %+v, %v", c, err)
	}

	// No file is the zero-config case (ADR-0009).
	empty := dataDir(t, "")
	c, err = load("serve", []string{"--data", empty}, noEnv, io.Discard)
	if err != nil || !reflect.DeepEqual(c, Config{DataDir: empty, Listen: ":8080"}) {
		t.Fatalf("no file = %+v, %v", c, err)
	}
}

func TestConfigFileRefusesWhatItCantUse(t *testing.T) {
	noEnv := func(string) string { return "" }
	for name, tc := range map[string]struct{ body, want string }{
		"a typo":               {"lisen: \":1\"\n", "lisen"},
		"the data directory":   {"data_dir: /elsewhere\n", "data_dir"},
		"malformed YAML":       {"listen: [\n", "config.yaml"},
		"a non-boolean":        {"allow_network_fs: maybe\n", "line 1"},
		"a non-boolean opt-in": {"allow_ephemeral_data: sure\n", "line 1"},
		"two documents":        {"listen: \":1\"\n---\nlisten: \":2\"\n", "one document"},
		"a wildcard host":      {"allowed_hosts: [\"*\"]\n", "allowed_hosts"},
		"a host with a port":   {"allowed_hosts: [\"notes.example.org:8080\"]\n", "allowed_hosts"},
		"a URL for a host":     {"allowed_hosts: [\"https://notes.example.org\"]\n", "allowed_hosts"},
		"an empty host":        {"allowed_hosts: [\"\"]\n", "allowed_hosts"},
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
