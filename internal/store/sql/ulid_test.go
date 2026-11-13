//go:build linux

package sql

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// A ULID (ADR-0031): 26 Crockford base32 characters, the first ten the milliseconds, the
// other sixteen 80 random bits read each time.
func TestULIDFormat(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_123)
	id, err := newULID(at, bytes.NewReader(bytes.Repeat([]byte{0xff}, 10)))
	if err != nil {
		t.Fatal(err)
	}
	// 1_700_000_000_123 ms in ten characters (checked with an independent encoder); ten 0xff bytes are sixteen Zs.
	if want := "01HF7YAT3V" + strings.Repeat("Z", 16); id != want {
		t.Fatalf("newULID = %q, want %q", id, want)
	}
	if !ValidID(id) {
		t.Fatalf("ValidID(%q) = false", id)
	}
	if got := idTime(id); !got.Equal(at) {
		t.Fatalf("the ID's time = %v, want %v", got, at)
	}
}

// Never the monotonic mode: two IDs in the same millisecond share no random part derived
// from the other (each reads its own 80 bits), so the second isn't the first plus one.
func TestULIDIsNotMonotonic(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_000)
	src := bytes.NewReader(append(bytes.Repeat([]byte{0x01}, 10), bytes.Repeat([]byte{0x01}, 10)...))
	a, err := newULID(at, src)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newULID(at, src)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("two IDs from the same random bytes differ (%q, %q): the second derived from the first", a, b)
	}
}

func TestULIDRandomness(t *testing.T) {
	seen := map[string]bool{}
	for range 10_000 {
		id := NewID(time.Now())
		if !ValidID(id) {
			t.Fatalf("NewID = %q, not a valid ID", id)
		}
		if seen[id] {
			t.Fatalf("NewID repeated %q", id)
		}
		seen[id] = true
	}
}

func TestULIDShortRandomRead(t *testing.T) {
	if _, err := newULID(time.Now(), bytes.NewReader([]byte{1, 2, 3})); err == nil {
		t.Fatal("newULID took fewer than 80 random bits")
	}
	if _, err := newULID(time.Now(), errReader{}); !errors.Is(err, errBroken) {
		t.Fatalf("newULID with a failing source = %v", err)
	}
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		"01HF7YAT1VZZZZZZZZZZZZZZZZ":  true,
		"7ZZZZZZZZZZZZZZZZZZZZZZZZZ":  true,  // the largest
		"8ZZZZZZZZZZZZZZZZZZZZZZZZZ":  false, // over 128 bits
		"01hf7yat1vzzzzzzzzzzzzzzzz":  false, // lower case is not what we write
		"01HF7YAT1VZZZZZZZZZZZZZZZ":   false, // 25
		"01HF7YAT1VZZZZZZZZZZZZZZZZZ": false, // 27
		"01HF7YAT1VZZZZZZZZZZZZZZZI":  false, // I, L, O, U aren't Crockford digits
		"01HF7YAT1VZZZZZZZZZZZZZZZU":  false,
		"../../../etc/passwd":         false,
		"":                            false,
	} {
		if got := ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", id, got, want)
		}
	}
}

var errBroken = errors.New("broken")

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errBroken }
