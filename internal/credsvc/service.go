package credsvc

import (
	"context"
	"errors"
	"fmt"

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
	return s.Store.Set(ctx, subject, string(hash))
}

// Exists reports whether a subject already has a local credential.
func (s *Service) Exists(ctx context.Context, subject string) (bool, error) {
	if subject == "" {
		return false, nil
	}
	return s.Store.Exists(ctx, subject)
}
