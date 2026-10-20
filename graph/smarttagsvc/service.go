package smarttagsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
)

// ErrInvalid is returned for a request the service does not accept.
var ErrInvalid = errors.New("invalid")

// ErrAnonymous is returned when the caller is not identified: a smart tag belongs to a user.
var ErrAnonymous = errors.New("smart tags need an identified user")

// ErrForbidden is returned when the caller reads a smart tag they may not change as asked.
var ErrForbidden = authz.ErrForbidden

// Limits of a smart tag: the service keeps what people give it, so it bounds its own storage.
const (
	MaxNameRunes        = 200
	MaxDescriptionRunes = 4000
	MaxVersions         = 1000
	MaxShares           = 200
)

// Patch changes a smart tag: a name (unchanged when empty), a description (when SetDescription), versions added to
// and removed from its selection.
type Patch struct {
	Name           string
	Description    string
	SetDescription bool
	Add, Remove    []domain.NodeRef
}

// Service reads and writes the smart tags of the callers.
type Service struct {
	Store Store
	// Exists checks that the versions a selection gains exist (nil: not checked); an error refuses the edit.
	Exists func(ctx context.Context, refs []domain.NodeRef) error
	// Now is the clock (time.Now by default).
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func caller(ctx context.Context) (string, error) {
	p := authz.From(ctx)
	if p.Anonymous() {
		return "", ErrAnonymous
	}
	return p.Subject, nil
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: a smart tag needs a name", ErrInvalid)
	}
	if utf8.RuneCountInString(name) > MaxNameRunes {
		return "", fmt.Errorf("%w: a name is at most %d characters", ErrInvalid, MaxNameRunes)
	}
	return name, nil
}

func checkDescription(d string) error {
	if utf8.RuneCountInString(d) > MaxDescriptionRunes {
		return fmt.Errorf("%w: a description is at most %d characters", ErrInvalid, MaxDescriptionRunes)
	}
	return nil
}

func checkRefs(refs []domain.NodeRef) error {
	for _, r := range refs {
		if r.ID == "" || r.Version <= 0 {
			return fmt.Errorf("%w: a smart tag selects written versions, not %s", ErrInvalid, r)
		}
	}
	return nil
}

// sorted returns the versions without duplicates, by node then version.
func sorted(refs []domain.NodeRef) []domain.NodeRef {
	out := slices.Clone(refs)
	slices.SortFunc(out, func(a, b domain.NodeRef) int {
		if c := strings.Compare(string(a.ID), string(b.ID)); c != 0 {
			return c
		}
		return int(a.Version) - int(b.Version)
	})
	return slices.Compact(out)
}

func (s *Service) exists(ctx context.Context, refs []domain.NodeRef) error {
	if s.Exists == nil || len(refs) == 0 {
		return nil
	}
	if err := s.Exists(ctx, refs); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// view is what subject sees of t: the owner every share, the others their own.
func view(t SmartTag, subject string) SmartTag {
	if subject != t.Owner {
		t.Shares = slices.DeleteFunc(t.Shares, func(s Share) bool { return s.Subject != subject })
	}
	return t
}

// Create makes a smart tag owned by the caller.
func (s *Service) Create(ctx context.Context, name, description string, versions []domain.NodeRef) (SmartTag, error) {
	who, err := caller(ctx)
	if err != nil {
		return SmartTag{}, err
	}
	if name, err = checkName(name); err != nil {
		return SmartTag{}, err
	}
	if err := checkDescription(description); err != nil {
		return SmartTag{}, err
	}
	if err := checkRefs(versions); err != nil {
		return SmartTag{}, err
	}
	versions = sorted(versions)
	if len(versions) > MaxVersions {
		return SmartTag{}, fmt.Errorf("%w: a smart tag selects at most %d versions", ErrInvalid, MaxVersions)
	}
	if err := s.exists(ctx, versions); err != nil {
		return SmartTag{}, err
	}
	now := s.now()
	t := SmartTag{ID: newID(), Name: name, Description: description, Owner: who, Versions: versions, By: who, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.Create(ctx, t); err != nil {
		return SmartTag{}, err
	}
	return t, nil
}

// Get returns a smart tag the caller owns or that is shared with them (ErrNotFound otherwise).
func (s *Service) Get(ctx context.Context, id string) (SmartTag, error) {
	who, err := caller(ctx)
	if err != nil {
		return SmartTag{}, err
	}
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return SmartTag{}, err
	}
	if t.AccessOf(who) == "" {
		return SmartTag{}, fmt.Errorf("smart tag %s: %w", id, ErrNotFound)
	}
	return view(t, who), nil
}

// List returns the smart tags the caller owns or that are shared with them, selecting a version of node when set.
func (s *Service) List(ctx context.Context, node domain.NodeID) ([]SmartTag, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ts, err := s.Store.List(ctx, who, node)
	for i := range ts {
		ts[i] = view(ts[i], who)
	}
	return ts, err
}

// change applies fn to a smart tag the caller has at least access want to.
func (s *Service) change(ctx context.Context, id, want string, fn func(t *SmartTag, who string) error) (SmartTag, error) {
	who, err := caller(ctx)
	if err != nil {
		return SmartTag{}, err
	}
	t, err := s.Store.Update(ctx, id, func(t *SmartTag) error {
		switch have := t.AccessOf(who); {
		case have == "":
			return fmt.Errorf("smart tag %s: %w", id, ErrNotFound)
		case want == AccessOwner && have != AccessOwner, want == AccessEdit && have == AccessRead:
			return fmt.Errorf("smart tag %s: %s access needed: %w", id, want, ErrForbidden)
		}
		if err := fn(t, who); err != nil {
			return err
		}
		t.By, t.UpdatedAt = who, s.now()
		return nil
	})
	if err != nil {
		return SmartTag{}, err
	}
	return view(t, who), nil
}

// Update renames, describes and edits the selection of a smart tag: its owner or an editor.
func (s *Service) Update(ctx context.Context, id string, p Patch) (SmartTag, error) {
	if err := checkRefs(p.Add); err != nil {
		return SmartTag{}, err
	}
	if p.SetDescription {
		if err := checkDescription(p.Description); err != nil {
			return SmartTag{}, err
		}
	}
	if p.Name != "" {
		name, err := checkName(p.Name)
		if err != nil {
			return SmartTag{}, err
		}
		p.Name = name
	}
	if err := s.exists(ctx, p.Add); err != nil {
		return SmartTag{}, err
	}
	return s.change(ctx, id, AccessEdit, func(t *SmartTag, _ string) error {
		if p.Name != "" {
			t.Name = p.Name
		}
		if p.SetDescription {
			t.Description = p.Description
		}
		vs := slices.DeleteFunc(append(t.Versions, p.Add...), func(r domain.NodeRef) bool { return slices.Contains(p.Remove, r) })
		vs = sorted(vs)
		if len(vs) > MaxVersions {
			return fmt.Errorf("%w: a smart tag selects at most %d versions", ErrInvalid, MaxVersions)
		}
		t.Versions = vs
		return nil
	})
}

// Share grants subject access to read or to edit a smart tag, or withdraws it (access ""): its owner only.
func (s *Service) Share(ctx context.Context, id, subject, access string) (SmartTag, error) {
	subject = strings.TrimSpace(subject)
	switch {
	case subject == "":
		return SmartTag{}, fmt.Errorf("%w: a share names a user", ErrInvalid)
	case access != "" && access != AccessRead && access != AccessEdit:
		return SmartTag{}, fmt.Errorf("%w: access is %q or %q, not %q", ErrInvalid, AccessRead, AccessEdit, access)
	}
	return s.change(ctx, id, AccessOwner, func(t *SmartTag, who string) error {
		if subject == who {
			return fmt.Errorf("%w: the owner of a smart tag needs no share", ErrInvalid)
		}
		t.Shares = slices.DeleteFunc(t.Shares, func(sh Share) bool { return sh.Subject == subject })
		if access != "" {
			if len(t.Shares) >= MaxShares {
				return fmt.Errorf("%w: a smart tag is shared with at most %d users", ErrInvalid, MaxShares)
			}
			t.Shares = append(t.Shares, Share{Subject: subject, Access: access})
			slices.SortFunc(t.Shares, func(a, b Share) int { return strings.Compare(a.Subject, b.Subject) })
		}
		return nil
	})
}

// Delete removes a smart tag: its owner only.
func (s *Service) Delete(ctx context.Context, id string) error {
	who, err := caller(ctx)
	if err != nil {
		return err
	}
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	switch t.AccessOf(who) {
	case "":
		return fmt.Errorf("smart tag %s: %w", id, ErrNotFound)
	case AccessOwner:
		return s.Store.Delete(ctx, id)
	}
	return fmt.Errorf("smart tag %s: only its owner deletes it: %w", id, ErrForbidden)
}

// VersionsExist is an Exists over a reader of node versions (the graph's Node): every version must be readable.
func VersionsExist(read func(context.Context, domain.NodeRef) (domain.Node, error)) func(context.Context, []domain.NodeRef) error {
	return func(ctx context.Context, refs []domain.NodeRef) error {
		for _, r := range refs {
			if _, err := read(ctx, r); err != nil {
				return fmt.Errorf("version %s: %w", r, err)
			}
		}
		return nil
	}
}
