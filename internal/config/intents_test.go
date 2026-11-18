package config

import (
	"io"
	"testing"
)

// TestResolveTakesOneOpID: intents resolve takes exactly one op ID, before or after the
// flags; every other command takes none.
func TestResolveTakesOneOpID(t *testing.T) {
	none := func(string) string { return "" }
	data := t.TempDir()
	for _, args := range [][]string{{"--data", data, "abc"}, {"abc", "--data", data}} {
		c, err := load("intents resolve", args, none, io.Discard)
		if err != nil || c.OpID != "abc" || c.DataDir != data {
			t.Fatalf("intents resolve %v = %+v, %v", args, c, err)
		}
	}
	for _, args := range [][]string{nil, {"--data", data}, {"abc", "def"}, {""}} {
		if _, err := load("intents resolve", args, none, io.Discard); err == nil {
			t.Fatalf("intents resolve %q: nil, want an error", args)
		}
	}
	for _, name := range []string{"intents list", "serve"} {
		if _, err := load(name, []string{"abc"}, none, io.Discard); err == nil {
			t.Fatalf("%s abc: nil, want an error", name)
		}
	}
}
