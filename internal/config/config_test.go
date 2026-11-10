package config

import (
	"io"
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
