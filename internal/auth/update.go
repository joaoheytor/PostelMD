package auth

import (
	"crypto/rand"
	"encoding/binary"

	"github.com/joaoheytor/PostelMD/internal/errattr"
)

// anyEpoch: update applies whatever the current epoch is (epochs start at 1).
const anyEpoch = 0

// update applies change to a copy of the credential, saves it, then makes it current. With
// an epoch, it is a compare-and-swap: ErrBadSession if the credential changed since then.
//
// Every change bumps the epoch, so it fails closed: when the save fails, the change is made
// current anyway, with its epoch jumped (jumpEpoch), held for a later save (retryUnsaved),
// and the error wraps ErrNotSaved. Keeping the old credential would leave the sessions it
// revokes (a stolen cookie, the old password) valid (#187, ADR-0038).
func (s *Service) update(epoch uint64, change func(*credential)) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cred == nil {
		return 0, ErrNotSetUp
	}
	if epoch != anyEpoch && s.cred.Epoch != epoch {
		return 0, ErrBadSession
	}
	c := *s.cred
	change(&c)
	if err := saveCredential(s.root, &c); err != nil {
		jumpEpoch(&c)
		s.cred = &c
		s.holdUnsaved(err)
		return 0, notSaved{err}
	}
	s.cred, s.unsaved = &c, false
	return c.Epoch, nil
}

// notSaved is ErrNotSaved with the save's error as its only cause, so the log can still
// name that error's type (errattr.Pathless follows a single chain).
type notSaved struct{ err error }

func (e notSaved) Error() string        { return ErrNotSaved.Error() + ": " + e.err.Error() }
func (e notSaved) Is(target error) bool { return target == ErrNotSaved }
func (e notSaved) Unwrap() error        { return e.err }

// jumpEpoch moves an epoch that wasn't saved forward by 1 + a random uint32. A restart
// goes back to the saved epoch, and the bumps after it count up from there; without the
// jump, the first one would land on the epoch of a cookie issued while the change was held
// only in memory, and bring that revoked cookie back.
func jumpEpoch(c *credential) {
	var b [4]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	c.Epoch += 1 + uint64(binary.BigEndian.Uint32(b[:]))
}

// holdUnsaved marks s.cred as held only in memory, warning once (without the error's
// text, which names the state directory) until it is saved. s.mu is held.
func (s *Service) holdUnsaved(err error) {
	if !s.unsaved {
		s.log.Warn("auth: a security change is held only in memory; a restart would undo it", errattr.Pathless(err))
	}
	s.unsaved = true
}

// retryUnsaved saves a credential held only in memory. It runs on every login and session
// check while one is held: one small atomic write, only until it succeeds. s.mu is held.
func (s *Service) retryUnsaved() {
	if !s.unsaved || s.cred == nil || saveCredential(s.root, s.cred) != nil {
		return
	}
	s.unsaved = false
	s.log.Info("auth: the security change held in memory is now saved")
}
