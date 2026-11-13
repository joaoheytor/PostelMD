//go:build linux

package sql

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

// File IDs are ULIDs (ADR-0031): 48 bits of milliseconds then 80 bits read from crypto/rand
// for each ID, 128 bits written as 26 Crockford base32 characters. Never the monotonic mode,
// which derives an ID from the last one made in the same millisecond. No dependency: the
// encoding is twenty lines.

// crockford is Crockford's base32 alphabet: no I, L, O or U.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// idLen is a ULID's length: 128 bits in 5-bit characters, the first holding 3.
const idLen = 26

// idRand is NewID's source of random bits: crypto/rand, which tests replace.
var idRand io.Reader = rand.Reader

// NewID returns a new file ID with at's milliseconds. crypto/rand.Read never fails (Go 1.24),
// so neither does NewID.
func NewID(at time.Time) string {
	id, err := newULID(at, idRand)
	if err != nil {
		panic(err) // crypto/rand's Reader doesn't return errors
	}
	return id
}

// newULID is NewID with the random bits read from src (tests use their own).
func newULID(at time.Time, src io.Reader) (string, error) {
	var b [16]byte
	ms := uint64(at.UnixMilli()) //nolint:gosec // G115: times before 1970 aren't made here
	// The low 48 bits, big-endian: the truncations are the point.
	binary.BigEndian.PutUint16(b[0:2], uint16(ms>>32)) //nolint:gosec // G115: see above
	binary.BigEndian.PutUint32(b[2:6], uint32(ms))     //nolint:gosec // G115: see above
	if _, err := io.ReadFull(src, b[6:]); err != nil {
		return "", fmt.Errorf("sql: read an ID's random bits: %w", err)
	}
	return encode(b), nil
}

// encode writes the 128 bits of b, most significant first, as 26 characters: the first
// takes the top 3 bits, each next one 5.
func encode(b [16]byte) string {
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [idLen]byte
	for i := idLen - 1; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}

// ValidID reports whether s is an ID as NewID writes it: 26 upper-case Crockford characters,
// the first at most 7 (128 bits). Callers check an ID a client sent before any lookup.
func ValidID(s string) bool {
	if len(s) != idLen || s[0] > '7' {
		return false
	}
	for i := range len(s) {
		if decode(s[i]) < 0 {
			return false
		}
	}
	return true
}

// decode is the value of the Crockford character c, or -1.
func decode(c byte) int {
	for i := range len(crockford) {
		if crockford[i] == c {
			return i
		}
	}
	return -1
}
