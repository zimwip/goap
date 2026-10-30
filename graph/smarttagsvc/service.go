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
	// MaxDepth is how many levels a tree of smart tags has at most, its root included (ADR 0178).
	MaxDepth = 16
)

// New is a smart tag to create: under a parent (a sub-tag, in its parent's project), else a root of the caller, of a
// project when one is named.
type New struct {
	Name, Description string
	Versions          []domain.NodeRef
	Parent, Project   string
}

// Patch changes a smart tag: a name (unchanged when empty), a description (when SetDescription), versions added to
// and removed from its selection, a new parent (when SetParent; "": a root of its own).
type Patch struct {
	Name           string
	Description    string
	SetDescription bool
	Add, Remove    []domain.NodeRef
	Parent         string
	SetParent      bool
}

// Service reads and writes the smart tags of the callers.
type Service struct {
	Store Store
	// Exists checks that the versions a selection gains exist (nil: not checked); an error refuses the edit. Version 0
	// names the node itself.
	Exists func(ctx context.Context, refs []domain.NodeRef) error
	// MayAccessProject reports whether the caller may work on a project (ADR 0039): the members of a project read and
	// edit its smart tags (ADR 0178). Nil: no smart tag belongs to a project.
	MayAccessProject func(ctx context.Context, project string) (bool, error)
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

// checkRefs accepts written versions and, at version 0, nodes (followed in the reader's view, ADR 0178).
func checkRefs(refs []domain.NodeRef) error {
	for _, r := range refs {
		if r.ID == "" || r.Version < 0 {
			return fmt.Errorf("%w: a smart tag selects nodes or written versions, not %s", ErrInvalid, r)
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

func (s *Service) mayAccess(ctx context.Context, project string) (bool, error) {
	if project == "" || s.MayAccessProject == nil {
		return false, nil
	}
	return s.MayAccessProject(ctx, project)
}

// tags reads smart tags by id, keeping what it read in known (the reads of one call).
type tags struct {
	s     *Service
	known map[string]SmartTag
}

func (s *Service) reader(ts ...SmartTag) *tags {
	r := &tags{s: s, known: map[string]SmartTag{}}
	for _, t := range ts {
		r.known[t.ID] = t
	}
	return r
}

func (r *tags) get(ctx context.Context, id string) (SmartTag, error) {
	if t, ok := r.known[id]; ok {
		return t, nil
	}
	t, err := r.s.Store.Get(ctx, id)
	if err == nil {
		r.known[id] = t
	}
	return t, err
}

// chain is t then its ancestors, its root last: at most MaxDepth tags, a cycle or a missing parent ending it.
func (r *tags) chain(ctx context.Context, t SmartTag) ([]SmartTag, error) {
	out := []SmartTag{t}
	for t.Parent != "" && len(out) < MaxDepth {
		p, err := r.get(ctx, t.Parent)
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(out, func(x SmartTag) bool { return x.ID == p.ID }) {
			break
		}
		out = append(out, p)
		t = p
	}
	return out, nil
}

// access is what subject may do with t: what the root of its tree grants (its owner, its shares), else edit for a
// member of the project of the tree.
func (r *tags) access(ctx context.Context, t SmartTag, subject string) (string, error) {
	chain, err := r.chain(ctx, t)
	if err != nil {
		return "", err
	}
	root := chain[len(chain)-1]
	if a := root.AccessOf(subject); a != "" {
		return a, nil
	}
	if ok, err := r.s.mayAccess(ctx, root.Project); err != nil || !ok {
		return "", err
	}
	return AccessEdit, nil
}

// view is what subject sees of t, with its access: the owner of a root every share, the others their own.
func view(t SmartTag, subject, access string) SmartTag {
	if subject != t.Owner {
		t.Shares = slices.DeleteFunc(t.Shares, func(s Share) bool { return s.Subject != subject })
	}
	t.Access = access
	return t
}

// height is how many levels the tree of t has below t.
func (s *Service) height(ctx context.Context, id string) (int, error) {
	level, h := []string{id}, 0
	for len(level) > 0 && h <= MaxDepth {
		ts, err := s.Store.Children(ctx, level)
		if err != nil {
			return 0, err
		}
		level = level[:0]
		for _, t := range ts {
			level = append(level, t.ID)
		}
		if len(level) > 0 {
			h++
		}
	}
	return h, nil
}

// Create makes a smart tag: a root owned by the caller (of a project the caller works on, when named), or a sub-tag of
// a tag the caller edits, in its project.
func (s *Service) Create(ctx context.Context, n New) (SmartTag, error) {
	who, err := caller(ctx)
	if err != nil {
		return SmartTag{}, err
	}
	name, err := checkName(n.Name)
	if err != nil {
		return SmartTag{}, err
	}
	if err := checkDescription(n.Description); err != nil {
		return SmartTag{}, err
	}
	if err := checkRefs(n.Versions); err != nil {
		return SmartTag{}, err
	}
	versions := sorted(n.Versions)
	if len(versions) > MaxVersions {
		return SmartTag{}, fmt.Errorf("%w: a smart tag selects at most %d versions", ErrInvalid, MaxVersions)
	}
	project, access := strings.TrimSpace(n.Project), AccessOwner
	if n.Parent != "" {
		r := s.reader()
		p, err := r.get(ctx, n.Parent)
		if err != nil {
			return SmartTag{}, err
		}
		have, err := r.access(ctx, p, who)
		if err != nil {
			return SmartTag{}, err
		}
		switch {
		case have == "":
			return SmartTag{}, fmt.Errorf("smart tag %s: %w", p.ID, ErrNotFound)
		case have == AccessRead:
			return SmartTag{}, fmt.Errorf("smart tag %s: edit access needed: %w", p.ID, ErrForbidden)
		case project != "" && project != p.Project:
			return SmartTag{}, fmt.Errorf("%w: a sub-tag is in the project of its parent (%q)", ErrInvalid, p.Project)
		}
		chain, err := r.chain(ctx, p)
		if err != nil {
			return SmartTag{}, err
		}
		if len(chain) >= MaxDepth {
			return SmartTag{}, fmt.Errorf("%w: a tree of smart tags has at most %d levels", ErrInvalid, MaxDepth)
		}
		project, access = p.Project, have
	} else if project != "" {
		if ok, err := s.mayAccess(ctx, project); err != nil {
			return SmartTag{}, err
		} else if !ok {
			return SmartTag{}, fmt.Errorf("project %s: %w", project, ErrForbidden)
		}
	}
	if err := s.exists(ctx, versions); err != nil {
		return SmartTag{}, err
	}
	now := s.now()
	t := SmartTag{ID: newID(), Name: name, Description: n.Description, Owner: who, Parent: n.Parent, Project: project, Versions: versions,
		By: who, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.Create(ctx, t); err != nil {
		return SmartTag{}, err
	}
	return view(t, who, access), nil
}

// Get returns a smart tag the caller reads (ErrNotFound otherwise).
func (s *Service) Get(ctx context.Context, id string) (SmartTag, error) {
	who, err := caller(ctx)
	if err != nil {
		return SmartTag{}, err
	}
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return SmartTag{}, err
	}
	a, err := s.reader(t).access(ctx, t, who)
	if err != nil {
		return SmartTag{}, err
	}
	if a == "" {
		return SmartTag{}, fmt.Errorf("smart tag %s: %w", id, ErrNotFound)
	}
	return view(t, who, a), nil
}

// List returns the smart tags the caller reads: those of project when named (the caller working on it), else the
// trees the caller owns or that are shared with them, whole; selecting node when set.
func (s *Service) List(ctx context.Context, node domain.NodeID, project string) ([]SmartTag, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	var ts []SmartTag
	switch {
	case project != "":
		if ok, err := s.mayAccess(ctx, project); err != nil {
			return nil, err
		} else if !ok {
			return nil, fmt.Errorf("project %s: %w", project, ErrForbidden)
		}
		ts, err = s.Store.InProject(ctx, project)
	case node != "":
		ts, err = s.Store.Selecting(ctx, node)
	default:
		ts, err = s.trees(ctx, who)
	}
	if err != nil {
		return nil, err
	}
	r := s.reader(ts...)
	out := ts[:0]
	for _, t := range ts {
		if node != "" && !t.Selects(node) {
			continue
		}
		a, err := r.access(ctx, t, who)
		if err != nil {
			return nil, err
		}
		if a != "" {
			out = append(out, view(t, who, a))
		}
	}
	return out, nil
}

// trees is what subject owns or is shared with, and every tag below it, by name.
func (s *Service) trees(ctx context.Context, subject string) ([]SmartTag, error) {
	ts, err := s.Store.List(ctx, subject)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	level := make([]string, 0, len(ts))
	for _, t := range ts {
		seen[t.ID] = true
		level = append(level, t.ID)
	}
	for depth := 0; len(level) > 0 && depth < MaxDepth; depth++ {
		below, err := s.Store.Children(ctx, level)
		if err != nil {
			return nil, err
		}
		level = level[:0]
		for _, t := range below {
			if !seen[t.ID] {
				seen[t.ID] = true
				level = append(level, t.ID)
				ts = append(ts, t)
			}
		}
	}
	sortTags(ts)
	return ts, nil
}

// change applies fn to a smart tag the caller has at least access want to (the access of its tree), after pre has
// checked the tag as read (pre reads the store; fn runs under its lock and reads nothing).
func (s *Service) change(ctx context.Context, id, want string, pre func(cur SmartTag, who string, r *tags) error, fn func(t *SmartTag, who string) error) (SmartTag, error) {
	who, err := caller(ctx)
	if err != nil {
		return SmartTag{}, err
	}
	cur, err := s.Store.Get(ctx, id)
	if err != nil {
		return SmartTag{}, err
	}
	r := s.reader(cur)
	have, err := r.access(ctx, cur, who)
	if err != nil {
		return SmartTag{}, err
	}
	switch {
	case have == "":
		return SmartTag{}, fmt.Errorf("smart tag %s: %w", id, ErrNotFound)
	case want == AccessOwner && have != AccessOwner, want == AccessEdit && have == AccessRead:
		return SmartTag{}, fmt.Errorf("smart tag %s: %s access needed: %w", id, want, ErrForbidden)
	}
	if pre != nil {
		if err := pre(cur, who, r); err != nil {
			return SmartTag{}, err
		}
	}
	t, err := s.Store.Update(ctx, id, func(t *SmartTag) error {
		if err := fn(t, who); err != nil {
			return err
		}
		t.By, t.UpdatedAt = who, s.now()
		return nil
	})
	if err != nil {
		return SmartTag{}, err
	}
	return view(t, who, have), nil
}

// Update renames, describes, edits the selection of and moves a smart tag: whoever edits its tree.
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
	var pre func(SmartTag, string, *tags) error
	if p.SetParent {
		pre = func(cur SmartTag, who string, r *tags) error {
			if p.Parent == cur.Parent {
				return nil
			}
			return s.checkMove(ctx, cur, p.Parent, who, r)
		}
	}
	return s.change(ctx, id, AccessEdit, pre, func(t *SmartTag, _ string) error {
		if p.Name != "" {
			t.Name = p.Name
		}
		if p.SetDescription {
			t.Description = p.Description
		}
		if p.SetParent {
			t.Parent = p.Parent
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

// checkMove checks that t may go under parent ("": a root of its own): a tag the caller edits, of the same project, not
// below t, the tree staying within MaxDepth levels. A sub-tag becomes a root by its creator only; a root keeping shares
// is not moved.
func (s *Service) checkMove(ctx context.Context, t SmartTag, parent, who string, r *tags) error {
	if parent == "" {
		if t.Owner != who {
			return fmt.Errorf("smart tag %s: a sub-tag becomes a root of its own by its creator only: %w", t.ID, ErrForbidden)
		}
		return nil
	}
	if t.Parent == "" && len(t.Shares) > 0 {
		return fmt.Errorf("%w: a root shared with others is not moved under another tag (withdraw its shares first)", ErrInvalid)
	}
	p, err := r.get(ctx, parent)
	if err != nil {
		return err
	}
	switch have, err := r.access(ctx, p, who); {
	case err != nil:
		return err
	case have == "":
		return fmt.Errorf("smart tag %s: %w", parent, ErrNotFound)
	case have == AccessRead:
		return fmt.Errorf("smart tag %s: edit access needed: %w", parent, ErrForbidden)
	case p.Project != t.Project:
		return fmt.Errorf("%w: a smart tag moves within its project (%q), not to %q", ErrInvalid, t.Project, p.Project)
	}
	chain, err := r.chain(ctx, p)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(chain, func(x SmartTag) bool { return x.ID == t.ID }) {
		return fmt.Errorf("%w: a smart tag is not moved below itself", ErrInvalid)
	}
	h, err := s.height(ctx, t.ID)
	if err != nil {
		return err
	}
	if len(chain)+1+h > MaxDepth {
		return fmt.Errorf("%w: a tree of smart tags has at most %d levels", ErrInvalid, MaxDepth)
	}
	return nil
}

// Share grants subject access to read or to edit a tree of smart tags, or withdraws it (access ""): the owner of its
// root, on the root.
func (s *Service) Share(ctx context.Context, id, subject, access string) (SmartTag, error) {
	subject = strings.TrimSpace(subject)
	switch {
	case subject == "":
		return SmartTag{}, fmt.Errorf("%w: a share names a user", ErrInvalid)
	case access != "" && access != AccessRead && access != AccessEdit:
		return SmartTag{}, fmt.Errorf("%w: access is %q or %q, not %q", ErrInvalid, AccessRead, AccessEdit, access)
	}
	return s.change(ctx, id, AccessOwner, nil, func(t *SmartTag, who string) error {
		if t.Parent != "" {
			return fmt.Errorf("%w: a sub-tag is shared with the root of its tree", ErrInvalid)
		}
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

// Delete removes a smart tag with no sub-tag: a root by its owner, a sub-tag by whoever edits its tree.
func (s *Service) Delete(ctx context.Context, id string) error {
	who, err := caller(ctx)
	if err != nil {
		return err
	}
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	have, err := s.reader(t).access(ctx, t, who)
	if err != nil {
		return err
	}
	switch {
	case have == "":
		return fmt.Errorf("smart tag %s: %w", id, ErrNotFound)
	case t.Parent == "" && have != AccessOwner:
		return fmt.Errorf("smart tag %s: only its owner deletes it: %w", id, ErrForbidden)
	case have == AccessRead:
		return fmt.Errorf("smart tag %s: edit access needed: %w", id, ErrForbidden)
	}
	below, err := s.Store.Children(ctx, []string{id})
	if err != nil {
		return err
	}
	if len(below) > 0 {
		return fmt.Errorf("%w: smart tag %s holds %d sub-tags: move or delete them first", ErrInvalid, id, len(below))
	}
	return s.Store.Delete(ctx, id)
}

// VersionsExist is an Exists over a reader of node versions (the graph's Node): every version must be readable, a
// node at version 0 its latest.
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
