package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/joaoheytor/PostelMD/internal/audit"
	"github.com/joaoheytor/PostelMD/internal/store/fs"
)

// SetupTokenTTL is how long the first-run setup token stays valid (ADR-0009).
const SetupTokenTTL = 30 * time.Minute

var (
	ErrNotSetUp     = errors.New("auth: setup hasn't run yet")
	ErrAlreadySetUp = errors.New("auth: already set up")
	ErrBadToken     = errors.New("auth: invalid or expired setup token")
	ErrBadPassword  = errors.New("auth: wrong password")
	ErrBadProfile   = errors.New("auth: the name and email must be set, without control characters")
)

// WaitError: this source must wait before trying a password again.
type WaitError struct{ Wait time.Duration }

func (e *WaitError) Error() string {
	return fmt.Sprintf("auth: too many attempts, retry in %s", e.Wait)
}

// Service is the single user's authentication.
type Service struct {
	root  *fs.Root // the state directory
	audit *audit.Log
	now   func() time.Time
	sess  *sessions
	bo    *backoff

	hashMu sync.Mutex // one argon2id at a time: each takes 64 MB

	ownsRoot bool // opened by OpenDir; closed by Close

	mu           sync.Mutex
	cred         *credential // nil before setup
	setupToken   string
	setupExpires time.Time
}

// Open loads the credential and the instance secret from the state directory.
func Open(root *fs.Root, a *audit.Log) (*Service, error) {
	secret, err := loadOrCreateSecret(root)
	if err != nil {
		return nil, err
	}
	cred, err := loadCredential(root)
	if err != nil {
		return nil, err
	}
	s := &Service{root: root, audit: a, now: time.Now, cred: cred}
	s.sess = newSessions(secret, func() time.Time { return s.now() })
	s.bo = newBackoff(func() time.Time { return s.now() })
	return s, nil
}

// OpenDir is Open on the state directory dir; Close releases it.
func OpenDir(dir string, a *audit.Log) (*Service, error) {
	root, err := fs.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	s, err := Open(root, a)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	s.ownsRoot = true
	return s, nil
}

// Close releases the state directory if OpenDir opened it.
func (s *Service) Close() error {
	if s.ownsRoot {
		return s.root.Close()
	}
	return nil
}

// NeedsSetup reports whether no password has been set yet.
func (s *Service) NeedsSetup() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cred == nil
}

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

// Setup sets the password and the git author with the setup token, and returns a session.
func (s *Service) Setup(ctx context.Context, token, password, name, email string) (string, error) {
	if err := checkProfile(name, email); err != nil {
		return "", err
	}
	hash, err := s.hash(password)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cred != nil {
		return "", ErrAlreadySetUp
	}
	if s.setupToken == "" || !s.now().Before(s.setupExpires) ||
		subtle.ConstantTimeCompare([]byte(token), []byte(s.setupToken)) != 1 {
		s.audit.Record(ctx, "setup.rejected")
		return "", ErrBadToken
	}
	c := &credential{PasswordHash: hash, Epoch: 1, Name: name, Email: email}
	if err := saveCredential(s.root, c); err != nil {
		return "", err
	}
	s.cred, s.setupToken = c, ""
	s.audit.Record(ctx, "setup.completed")
	return s.sess.issue(c.Epoch), nil
}

// Login checks the password for a source IP and returns a session.
func (s *Service) Login(ctx context.Context, ip, password string) (string, error) {
	epoch, err := s.verify(ctx, ip, password)
	if err != nil {
		return "", err
	}
	s.audit.Record(ctx, "login.succeeded", slog.String("ip", ip))
	return s.sess.issue(epoch), nil
}

// Check verifies a session cookie on every request (CLAUDE.md rule 16).
func (s *Service) Check(cookie string) error {
	s.mu.Lock()
	cred := s.cred
	s.mu.Unlock()
	if cred == nil {
		return ErrNotSetUp
	}
	return s.sess.check(cookie, cred.Epoch)
}

// ChangePassword needs a valid session and the current password (ADR-0020), so a stolen
// cookie alone can't lock the owner out. It revokes every session and returns a new one.
func (s *Service) ChangePassword(ctx context.Context, ip, cookie, current, next string) (string, error) {
	if err := s.Check(cookie); err != nil {
		return "", err
	}
	if _, err := s.verify(ctx, ip, current); err != nil {
		return "", err
	}
	hash, err := s.hash(next)
	if err != nil {
		return "", err
	}
	epoch, err := s.update(func(c *credential) { c.PasswordHash = hash; c.Epoch++ })
	if err != nil {
		return "", err
	}
	s.audit.Record(ctx, "password.changed", slog.String("ip", ip))
	return s.sess.issue(epoch), nil
}

// LogoutEverywhere revokes every session, including the caller's.
func (s *Service) LogoutEverywhere(ctx context.Context, cookie string) error {
	if err := s.Check(cookie); err != nil {
		return err
	}
	if _, err := s.update(func(c *credential) { c.Epoch++ }); err != nil {
		return err
	}
	s.audit.Record(ctx, "sessions.revoked")
	return nil
}

// Author returns the git author set during setup.
func (s *Service) Author() (name, email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cred == nil {
		return "", ""
	}
	return s.cred.Name, s.cred.Email
}

// verify checks a password under the per-IP backoff and returns the current epoch.
func (s *Service) verify(ctx context.Context, ip, password string) (uint64, error) {
	if d := s.bo.wait(ip); d > 0 {
		s.audit.Record(ctx, "login.throttled", slog.String("ip", ip))
		return 0, &WaitError{Wait: d}
	}
	s.mu.Lock()
	cred := s.cred
	s.mu.Unlock()
	if cred == nil {
		return 0, ErrNotSetUp
	}
	s.hashMu.Lock()
	ok, err := VerifyPassword(cred.PasswordHash, password)
	s.hashMu.Unlock()
	if err != nil {
		return 0, err
	}
	if !ok {
		s.bo.fail(ip)
		s.audit.Record(ctx, "login.failed", slog.String("ip", ip))
		return 0, ErrBadPassword
	}
	s.bo.success(ip)
	return cred.Epoch, nil
}

func (s *Service) hash(password string) (string, error) {
	s.hashMu.Lock()
	defer s.hashMu.Unlock()
	return HashPassword(password)
}

// update applies change to a copy of the credential, saves it, then makes it current.
func (s *Service) update(change func(*credential)) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cred == nil {
		return 0, ErrNotSetUp
	}
	c := *s.cred
	change(&c)
	if err := saveCredential(s.root, &c); err != nil {
		return 0, err
	}
	s.cred = &c
	return c.Epoch, nil
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

// ResetPassword sets a new password offline (`postelmd reset-password`, run with the
// server stopped and the instance lock held) and revokes every session. It keeps the git
// author, or fails with ErrNotSetUp before setup.
func ResetPassword(root *fs.Root, password string) error {
	cred, err := loadCredential(root)
	if err != nil {
		return err
	}
	if cred == nil {
		return ErrNotSetUp
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	cred.PasswordHash, cred.Epoch = hash, cred.Epoch+1
	return saveCredential(root, cred)
}
