package credsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ErrInvalid is returned for a request the service does not accept (empty subject/password, too short).
var ErrInvalid = errors.New("invalid")

// MinPasswordLen is the shortest password Register/SetPassword accepts.
const MinPasswordLen = 8

func validate(subject, password string) error {
	if subject == "" {
		return fmt.Errorf("%w: subject required", ErrInvalid)
	}
	if len(password) < MinPasswordLen {
		return fmt.Errorf("%w: password must be at least %d characters", ErrInvalid, MinPasswordLen)
	}
	return nil
}

// Service registers and verifies local credentials.
type Service struct{ Store Store }

// Register creates the credential of a new subject. ErrExists if one is already there.
func (s *Service) Register(ctx context.Context, subject, password string) error {
	if err := validate(subject, password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.Store.Create(ctx, subject, string(hash))
}

// Verify reports whether password matches the stored hash of subject. A subject with no credential (or a
// wrong password) is simply "no match" — the caller cannot tell which, so a login attempt gives no signal
// about which subjects have registered.
func (s *Service) Verify(ctx context.Context, subject, password string) (bool, error) {
	if subject == "" || password == "" {
		return false, nil
	}
	hash, err := s.Store.Hash(ctx, subject)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil, nil
}

// SetPassword replaces the password of a subject that already has a credential.
func (s *Service) SetPassword(ctx context.Context, subject, password string) error {
	if err := validate(subject, password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := s.Store.Set(ctx, subject, string(hash)); err != nil {
		return err
	}
	// a new password ends every session signed in with the old one (ADR 0045)
	return s.EndSessions(ctx, subject)
}

// Exists reports whether a subject already has a local credential.
func (s *Service) Exists(ctx context.Context, subject string) (bool, error) {
	if subject == "" {
		return false, nil
	}
	return s.Store.Exists(ctx, subject)
}

// StartSession opens a sign-in session of a subject (ADR 0045), valid maxAge, and returns its id: every token issued
// from this sign-in carries it, and stops being accepted once the session ends.
func (s *Service) StartSession(ctx context.Context, subject string, maxAge time.Duration) (string, error) {
	if subject == "" || maxAge <= 0 {
		return "", fmt.Errorf("%w: subject and session duration required", ErrInvalid)
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	now := time.Now()
	sess := Session{ID: hex.EncodeToString(b), Subject: subject, Created: now, Expires: now.Add(maxAge)}
	// sessions expired for a day are forgotten as new ones open
	if err := s.Store.CreateSession(ctx, sess, now.Add(-24*time.Hour)); err != nil {
		return "", err
	}
	return sess.ID, nil
}

// SessionActive reports whether a session of subject still accepts its tokens: it exists, belongs to subject, was
// not ended and has not expired.
func (s *Service) SessionActive(ctx context.Context, id, subject string) (bool, error) {
	if id == "" {
		return false, nil
	}
	sess, err := s.Store.Session(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sess.Subject == subject && sess.Active(time.Now()), nil
}

// EndSession ends a session (signing out): its tokens are refused from now on.
func (s *Service) EndSession(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	return s.Store.RevokeSession(ctx, id, time.Now())
}

// EndSessions ends every session of a subject (signing out everywhere, a new password).
func (s *Service) EndSessions(ctx context.Context, subject string) error {
	if subject == "" {
		return nil
	}
	return s.Store.RevokeSessions(ctx, subject, time.Now())
}
