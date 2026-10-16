package prefssvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
)

// ErrInvalid is returned for a document the service does not accept.
var ErrInvalid = errors.New("invalid")

// ErrAnonymous is returned when the caller is not identified: preferences belong to a declared user.
var ErrAnonymous = errors.New("preferences need an identified user")

// MaxDocumentBytes bounds the JSON encoding of a subject's document: the service keeps it opaque, so the only thing it
// protects is its own storage.
const MaxDocumentBytes = 64 << 10

// check refuses a document that is not a JSON object the service can keep: values of other kinds than JSON's, or
// larger than MaxDocumentBytes. What the keys mean, and which values they accept, is the business of the interface
// reading them (web/src/lib/stores/preferences.svelte.ts), never of this service (ADR 0068).
func check(doc Prefs) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("%w: not a JSON object: %v", ErrInvalid, err)
	}
	if len(b) > MaxDocumentBytes {
		return fmt.Errorf("%w: the document is %d bytes, at most %d", ErrInvalid, len(b), MaxDocumentBytes)
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

// Set merges a patch into the document of a subject: a nil value clears the key. Any key is kept; the result must
// stay a JSON object of at most MaxDocumentBytes.
func (s *Service) Set(ctx context.Context, subject string, patch Prefs) (Prefs, error) {
	if subject == "" {
		return nil, ErrAnonymous
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
		if err := check(out); err != nil {
			return nil, err
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
