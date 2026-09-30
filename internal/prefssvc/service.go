package prefssvc

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// ErrInvalid is returned for a document the service does not accept.
var ErrInvalid = errors.New("invalid")

// ErrAnonymous is returned when the caller is not identified: preferences belong to a declared user.
var ErrAnonymous = errors.New("preferences need an identified user")

type kind int

const (
	kindBool kind = iota
	kindEnum
)

type field struct {
	kind   kind
	values []string // the accepted values of an enum
}

// Schema lists the preferences a user can set. An unknown key or value is refused, so the document stays something
// the interface understands; a new preference is added here.
var Schema = map[string]field{
	"theme":         {kind: kindEnum, values: []string{"auto", "light", "dark"}},
	"voiceEnabled":  {kind: kindBool},
	"voiceModel":    {kind: kindEnum, values: []string{"tiny", "base"}},
	"voiceLanguage": {kind: kindEnum, values: []string{"auto", "fr", "en"}},
	// default period and scope of the token usage dashboard
	"usagePeriod": {kind: kindEnum, values: []string{"24h", "7d", "30d", "all"}},
	"usageScope":  {kind: kindEnum, values: []string{"mine", "platform"}},
}

// Validate checks the values of a patch; a nil value (clear the key) is always accepted for a known key.
func Validate(patch Prefs) error {
	for k, v := range patch {
		f, ok := Schema[k]
		if !ok {
			return fmt.Errorf("%w: unknown preference %q", ErrInvalid, k)
		}
		if v == nil {
			continue
		}
		switch f.kind {
		case kindBool:
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%w: %s must be true or false", ErrInvalid, k)
			}
		case kindEnum:
			s, ok := v.(string)
			if !ok || !slices.Contains(f.values, s) {
				return fmt.Errorf("%w: %s must be one of %v", ErrInvalid, k, f.values)
			}
		}
	}
	return nil
}

// Service reads and writes the preferences of a subject.
type Service struct{ Store Store }

// Get returns the preferences of a subject.
func (s *Service) Get(ctx context.Context, subject string) (Prefs, error) {
	if subject == "" {
		return nil, ErrAnonymous
	}
	return s.Store.Get(ctx, subject)
}

// Set merges a patch into the preferences of a subject: a nil value clears the key.
func (s *Service) Set(ctx context.Context, subject string, patch Prefs) (Prefs, error) {
	if subject == "" {
		return nil, ErrAnonymous
	}
	if err := Validate(patch); err != nil {
		return nil, err
	}
	return s.Store.Update(ctx, subject, func(cur Prefs) (Prefs, error) {
		out := maps.Clone(cur)
		if out == nil {
			out = Prefs{}
		}
		for k, v := range patch {
			if v == nil {
				delete(out, k)
			} else {
				out[k] = v
			}
		}
		return out, nil
	})
}

// Reset forgets the preferences of a subject.
func (s *Service) Reset(ctx context.Context, subject string) error {
	if subject == "" {
		return ErrAnonymous
	}
	return s.Store.Delete(ctx, subject)
}
