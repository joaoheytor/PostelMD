package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"log/slog"
	"strings"
	"time"
	"unicode"
)

// NewSetupToken returns a fresh single-use setup token, valid for SetupTokenTTL, replacing
// any earlier one. The caller prints it to the log only.
func (s *Service) NewSetupToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cred != nil {
		return "", ErrAlreadySetUp
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s.setupToken = base64.RawURLEncoding.EncodeToString(b)
	s.setupExpires = s.now().Add(SetupTokenTTL)
	return s.setupToken, nil
}

// CheckSetupToken reports whether token is the current, unexpired setup token
// (ErrBadToken otherwise, ErrAlreadySetUp after setup). It doesn't use the token up.
func (s *Service) CheckSetupToken(ctx context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkToken(ctx, token)
}

// checkToken is CheckSetupToken with s.mu held.
func (s *Service) checkToken(ctx context.Context, token string) error {
	if s.cred != nil {
		return ErrAlreadySetUp
	}
	if s.setupToken == "" || !s.now().Before(s.setupExpires) ||
		subtle.ConstantTimeCompare([]byte(token), []byte(s.setupToken)) != 1 {
		s.recordRejection(ctx)
		return ErrBadToken
	}
	return nil
}

// rejectionLogEvery bounds the setup.rejected records: anyone can send bad tokens.
const rejectionLogEvery = time.Minute

// recordRejection audits a bad setup token, at most once per rejectionLogEvery, with the
// number of rejections left out since the last record. s.mu is held.
func (s *Service) recordRejection(ctx context.Context) {
	if now := s.now(); s.rejectLogged.IsZero() || now.Sub(s.rejectLogged) >= rejectionLogEvery {
		s.audit.Record(ctx, "setup.rejected", slog.Int("suppressed", s.rejectSuppressed))
		s.rejectLogged, s.rejectSuppressed = now, 0
		return
	}
	s.rejectSuppressed++
}

// Setup sets the password and the git author with the setup token, and returns a session.
// The token is checked before the 64 MiB hash, so the setup route, which stays open, can't
// be used to queue hashes, and again after it, as a parallel setup may have finished.
func (s *Service) Setup(ctx context.Context, token, password, name, email string) (string, error) {
	if err := checkProfile(name, email); err != nil {
		return "", err
	}
	if err := s.CheckSetupToken(ctx, token); err != nil {
		return "", err
	}
	hash, err := s.hash(password)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkToken(ctx, token); err != nil {
		return "", err
	}
	c := &credential{PasswordHash: hash, Epoch: 1, Name: name, Email: email}
	if err := saveCredential(s.root, c); err != nil {
		return "", err
	}
	s.cred, s.setupToken = c, ""
	s.audit.Record(ctx, "setup.completed")
	return s.sess.issue(c.Epoch), nil
}

func checkProfile(name, email string) error {
	bad := func(v string, max int) bool {
		return strings.TrimSpace(v) == "" || len(v) > max || strings.IndexFunc(v, unicode.IsControl) >= 0
	}
	if bad(name, 100) || bad(email, 254) || !strings.Contains(email, "@") || strings.ContainsAny(email+name, "<>") {
		return ErrBadProfile
	}
	return nil
}
