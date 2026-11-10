// Package auth is the MVP's single-user authentication (ADR-0020): an argon2id password,
// a one-time setup token, signed session cookies with a revocation epoch, and per-IP login
// backoff. Its files live in the state directory and are written through internal/store/fs.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters, sized for small nodes (ADR-0005: ~64 MB, 1–2 passes).
const (
	argonMemory  = 64 << 10 // KiB
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16
)

// Password length limits, in characters. The upper bound keeps hashing cheap to request.
const (
	MinPasswordLen = 12
	MaxPasswordLen = 256
)

var (
	ErrWeakPassword = fmt.Errorf("auth: the password must be %d to %d characters", MinPasswordLen, MaxPasswordLen)
	errBadHash      = errors.New("auth: unrecognized password hash")
)

// CheckPassword enforces the length policy.
func CheckPassword(pw string) error {
	if n := utf8.RuneCountInString(pw); n < MinPasswordLen || n > MaxPasswordLen || !utf8.ValidString(pw) {
		return ErrWeakPassword
	}
	return nil
}

// HashPassword returns the PHC-encoded argon2id hash of pw.
func HashPassword(pw string) (string, error) {
	if err := CheckPassword(pw); err != nil {
		return "", err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches the encoded hash, in constant time for a given
// hash. The parameters come from the hash, so older hashes keep verifying if they change.
func VerifyPassword(encoded, pw string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false, errBadHash
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || m > 1<<20 || t == 0 || t > 10 || p == 0 {
		return false, errBadHash
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) == 0 || len(want) == 0 || len(want) > 64 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want))) //nolint:gosec // G115: len(want) <= 64, checked above
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
