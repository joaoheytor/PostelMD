package config

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestDataDirIsAbsolute: the data directory leaves config absolute and clean, from a flag or
// the environment, so store/sql (which refuses relative database paths) and every later
// command resolve it the same way (ADR-0030, follow-up 2).
func TestDataDirIsAbsolute(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	none := func(string) string { return "" }
	for _, tt := range []struct {
		name string
		args []string
		env  string
		want string
	}{
		{"a relative flag", []string{"--data", "rel/data"}, "", filepath.Join(wd, "rel", "data")},
		{"a relative environment value", nil, "./data/", filepath.Join(wd, "data")},
		{"an absolute path is cleaned", []string{"--data", "/srv//pm/../postelmd/"}, "", "/srv/postelmd"},
		{"the default", nil, "", "/data"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			get := none
			if tt.env != "" {
				get = func(k string) string {
					if k == "POSTELMD_DATA_DIR" {
						return tt.env
					}
					return ""
				}
			}
			c, err := load("serve", tt.args, get, io.Discard)
			if err != nil || c.DataDir != tt.want {
				t.Fatalf("DataDir = %q, %v; want %q", c.DataDir, err, tt.want)
			}
		})
	}
}
