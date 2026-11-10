package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

// SessionTTL is a session's absolute lifetime (ADR-0020).
const SessionTTL = 7 * 24 * time.Hour

const sessionVersion = 1

// ErrBadSession: the cookie is malformed, forged, expired or revoked.
var ErrBadSession = errors.New("auth: invalid session")

// sessions signs and checks session cookies. A cookie is
// base64url(version | epoch | expiry) "." base64url(HMAC-SHA256). The MAC key is derived
// from the instance secret for this one purpose.
type sessions struct {
	key []byte
	now func() time.Time
}

func newSessions(secret []byte, now func() time.Time) *sessions {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("postelmd session v1"))
	return &sessions{key: m.Sum(nil), now: now}
}

func (s *sessions) mac(payload []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write(payload)
	return m.Sum(nil)
}

// issue returns a cookie value valid for SessionTTL under epoch.
func (s *sessions) issue(epoch uint64) string {
	payload := make([]byte, 17)
	payload[0] = sessionVersion
	binary.BigEndian.PutUint64(payload[1:9], epoch)
	binary.BigEndian.PutUint64(payload[9:17], uint64(s.now().Add(SessionTTL).Unix())) //nolint:gosec // G115: a future Unix time is positive
	b64 := base64.RawURLEncoding
	return b64.EncodeToString(payload) + "." + b64.EncodeToString(s.mac(payload))
}

// check verifies a cookie value against the current epoch.
func (s *sessions) check(value string, epoch uint64) error {
	p, m, ok := strings.Cut(value, ".")
	if !ok {
		return ErrBadSession
	}
	// Strict: non-zero padding bits are refused, so a cookie has exactly one encoding.
	b64 := base64.RawURLEncoding.Strict()
	payload, err1 := b64.DecodeString(p)
	mac, err2 := b64.DecodeString(m)
	if err1 != nil || err2 != nil || len(payload) != 17 || !hmac.Equal(mac, s.mac(payload)) {
		return ErrBadSession
	}
	expiry := int64(binary.BigEndian.Uint64(payload[9:17])) //nolint:gosec // G115: signed by us; a wrapped value is just expired
	if payload[0] != sessionVersion || binary.BigEndian.Uint64(payload[1:9]) != epoch || s.now().Unix() >= expiry {
		return ErrBadSession
	}
	return nil
}
