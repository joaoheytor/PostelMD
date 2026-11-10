package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=2,p=1$") {
		t.Fatalf("hash %q", h)
	}
	for pw, want := range map[string]bool{"correct horse battery": true, "correct horse batterY": false, "": false} {
		if ok, err := VerifyPassword(h, pw); err != nil || ok != want {
			t.Errorf("VerifyPassword(%q) = %v, %v; want %v", pw, ok, err, want)
		}
	}
	if h2, _ := HashPassword("correct horse battery"); h2 == h {
		t.Fatal("two hashes of one password are equal: the salt isn't random")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=65536,t=2,p=1$c2FsdA$aGFzaA", "$argon2id$v=19$m=0,t=2,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=99999999,t=2,p=1$c2FsdA$aGFzaA", "$argon2id$v=19$m=65536,t=2,p=1$!!$aGFzaA"} {
		if _, err := VerifyPassword(bad, "x"); err == nil {
			t.Errorf("VerifyPassword accepted the hash %q", bad)
		}
	}
	for _, pw := range []string{"short", strings.Repeat("x", MaxPasswordLen+1), "\xff\xfe invalid utf8 xx"} {
		if _, err := HashPassword(pw); !errors.Is(err, ErrWeakPassword) {
			t.Errorf("HashPassword(%q) = %v, want ErrWeakPassword", pw, err)
		}
	}
}

func TestSessions(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newSessions([]byte("0123456789abcdef0123456789abcdef"), func() time.Time { return now })
	c := s.issue(3)
	if err := s.check(c, 3); err != nil {
		t.Fatalf("a fresh cookie: %v", err)
	}
	if err := s.check(c, 4); !errors.Is(err, ErrBadSession) {
		t.Fatal("a cookie survived an epoch bump")
	}
	other := newSessions([]byte("another instance secret, 32 byte"), s.now)
	if err := other.check(c, 3); !errors.Is(err, ErrBadSession) {
		t.Fatal("a cookie verified under another secret")
	}
	p, m, _ := strings.Cut(c, ".")
	flip := func(s string, i int) string {
		b := []byte(s)
		b[i] = map[bool]byte{true: 'B', false: 'A'}[b[i] == 'A']
		return string(b)
	}
	tampered := flip(p, 2) + "." + m       // changes the epoch bytes
	padding := flip(p, len(p)-1) + "." + m // the same bytes in a non-canonical encoding
	for _, bad := range []string{"", "x", c + "x", "." + m, p + ".", tampered, padding} {
		if err := s.check(bad, 3); !errors.Is(err, ErrBadSession) {
			t.Errorf("check(%q) accepted", bad)
		}
	}
	now = now.Add(SessionTTL)
	if err := s.check(c, 3); !errors.Is(err, ErrBadSession) {
		t.Fatal("an expired cookie was accepted")
	}
}
