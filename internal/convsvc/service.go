package convsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/authz"
)

// Errors of the Service.
var (
	// ErrInvalid is returned for a request the service does not accept (malformed, or beyond a limit).
	ErrInvalid = errors.New("invalid")
	// ErrAnonymous is returned when the caller is not identified: conversations belong to a declared user.
	ErrAnonymous = errors.New("conversations need an identified caller")
	// ErrForbidden is returned when the caller may not do that on a conversation they can see.
	ErrForbidden = errors.New("forbidden")
)

// Limits: they protect the storage of the service, not the use of the assistant.
const (
	MaxConversationsPerSubject = 200
	MaxMessagesPerConversation = 500
	MaxTextBytes               = 64 << 10
	MaxActionsBytes            = 64 << 10
	MaxTitleBytes              = 200
	MaxErrorBytes              = 4 << 10
	// MaxContextBytes caps the description of the context of a user message.
	MaxContextBytes = 300
	DefaultPageSize = 50
	MaxPageSize     = 200
	// DefaultTitle names a conversation created without a title.
	DefaultTitle = "New conversation"
)

// Service reads and writes the conversations of the callers.
//
// Who may do what: the owner of a conversation lists, reads, renames, deletes it and appends user messages to it. A
// platform service (a system principal, the executor of the assistant) appends assistant messages to any conversation
// and updates them, but reads no conversation and lists none. A conversation of someone else answers as if it did not
// exist.
type Service struct {
	Store Store
	// Now is the clock (time.Now when nil); NewID makes the ids (random when nil).
	Now   func() time.Time
	NewID func() string
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (s *Service) newID(prefix string) string {
	if s.NewID != nil {
		return s.NewID()
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (s *Service) storeErr(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return err
	case errors.Is(err, ErrFull):
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return err
}

// owned returns a conversation of the caller; one of someone else (or none) is not found.
func (s *Service) owned(ctx context.Context, p authz.Principal, id string) (Conversation, error) {
	if p.Anonymous() {
		return Conversation{}, ErrAnonymous
	}
	if p.System() || id == "" {
		return Conversation{}, ErrNotFound
	}
	c, err := s.Store.Get(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	if c.Subject != p.Subject {
		return Conversation{}, ErrNotFound
	}
	return c, nil
}

func checkTitle(t string) (string, error) {
	t = strings.TrimSpace(t)
	if len(t) > MaxTitleBytes {
		return "", fmt.Errorf("%w: the title is %d bytes, at most %d", ErrInvalid, len(t), MaxTitleBytes)
	}
	return t, nil
}

// Create opens an empty conversation owned by the caller (a person: a platform service has none).
func (s *Service) Create(ctx context.Context, p authz.Principal, title string) (Conversation, error) {
	if p.Anonymous() {
		return Conversation{}, ErrAnonymous
	}
	if p.System() {
		return Conversation{}, ErrForbidden
	}
	title, err := checkTitle(title)
	if err != nil {
		return Conversation{}, err
	}
	if title == "" {
		title = DefaultTitle
	}
	now := s.now()
	c := Conversation{ID: s.newID("CONV-"), Subject: p.Subject, Title: title, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.Create(ctx, c, MaxConversationsPerSubject); err != nil {
		return Conversation{}, s.storeErr(err)
	}
	return c, nil
}

// List returns a page of the conversations of the caller, most recently updated first, and the token of the next
// page ("" on the last one).
func (s *Service) List(ctx context.Context, p authz.Principal, pageSize int, token string) ([]Conversation, string, error) {
	if p.Anonymous() {
		return nil, "", ErrAnonymous
	}
	if p.System() {
		return nil, "", ErrForbidden
	}
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	pageSize = min(pageSize, MaxPageSize)
	after, err := parseToken(token)
	if err != nil {
		return nil, "", err
	}
	out, err := s.Store.List(ctx, p.Subject, after, pageSize+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > pageSize {
		out = out[:pageSize]
		last := out[len(out)-1]
		next = strconv.FormatInt(micros(last.UpdatedAt), 10) + "." + last.ID
	}
	return out, next, nil
}

func parseToken(t string) (Cursor, error) {
	if t == "" {
		return Cursor{}, nil
	}
	ts, id, ok := strings.Cut(t, ".")
	n, err := strconv.ParseInt(ts, 10, 64)
	if !ok || err != nil || id == "" {
		return Cursor{}, fmt.Errorf("%w: page token", ErrInvalid)
	}
	return Cursor{UpdatedAt: fromMicros(n), ID: id}, nil
}

// Get returns a conversation of the caller with its messages.
func (s *Service) Get(ctx context.Context, p authz.Principal, id string) (Conversation, []Message, error) {
	c, err := s.owned(ctx, p, id)
	if err != nil {
		return Conversation{}, nil, err
	}
	ms, err := s.Store.Messages(ctx, id)
	return c, ms, err
}

// Rename retitles a conversation of the caller.
func (s *Service) Rename(ctx context.Context, p authz.Principal, id, title string) (Conversation, error) {
	if _, err := s.owned(ctx, p, id); err != nil {
		return Conversation{}, err
	}
	title, err := checkTitle(title)
	if err != nil {
		return Conversation{}, err
	}
	if title == "" {
		return Conversation{}, fmt.Errorf("%w: a title is needed", ErrInvalid)
	}
	return s.Store.Rename(ctx, id, title, s.now())
}

// Delete forgets a conversation of the caller and its messages.
func (s *Service) Delete(ctx context.Context, p authz.Principal, id string) error {
	if _, err := s.owned(ctx, p, id); err != nil {
		return err
	}
	return s.Store.Delete(ctx, id)
}

// Content is what a message says.
type Content struct {
	Text      string
	Actions   []Action
	ProcessID string
	Status    string
	Error     string
	// Context describes, in a few words, what the user was looking at (user messages only).
	Context string
}

func (c Content) check() error {
	if len(c.Text) > MaxTextBytes {
		return fmt.Errorf("%w: the text is %d bytes, at most %d", ErrInvalid, len(c.Text), MaxTextBytes)
	}
	if len(c.Context) > MaxContextBytes {
		return fmt.Errorf("%w: the context is %d bytes, at most %d", ErrInvalid, len(c.Context), MaxContextBytes)
	}
	if len(c.Error) > MaxErrorBytes {
		return fmt.Errorf("%w: the error is %d bytes, at most %d", ErrInvalid, len(c.Error), MaxErrorBytes)
	}
	switch c.Status {
	case StatusPending, StatusDone, StatusError:
	default:
		return fmt.Errorf("%w: status %q is not pending, done or error", ErrInvalid, c.Status)
	}
	for i, a := range c.Actions {
		if t, _ := a["type"].(string); t == "" {
			return fmt.Errorf("%w: action %d has no type", ErrInvalid, i)
		}
	}
	if c.Actions != nil {
		b, err := json.Marshal(c.Actions)
		if err != nil {
			return fmt.Errorf("%w: actions are not JSON: %v", ErrInvalid, err)
		}
		if len(b) > MaxActionsBytes {
			return fmt.Errorf("%w: the actions are %d bytes, at most %d", ErrInvalid, len(b), MaxActionsBytes)
		}
	}
	return nil
}

// Append adds a message to a conversation: a user message by its owner (always done, no actions), an assistant
// message by a platform service. An empty role is a user message; an empty status is done.
func (s *Service) Append(ctx context.Context, p authz.Principal, conversationID, role string, c Content) (Message, error) {
	if p.Anonymous() {
		return Message{}, ErrAnonymous
	}
	if role == "" {
		role = RoleUser
	}
	if c.Status == "" {
		c.Status = StatusDone
	}
	switch role {
	case RoleUser:
		if _, err := s.owned(ctx, p, conversationID); err != nil {
			return Message{}, err
		}
		if len(c.Actions) > 0 || c.ProcessID != "" || c.Status != StatusDone || c.Error != "" {
			return Message{}, fmt.Errorf("%w: a user message carries a text only", ErrInvalid)
		}
		if strings.TrimSpace(c.Text) == "" {
			return Message{}, fmt.Errorf("%w: a user message needs a text", ErrInvalid)
		}
	case RoleAssistant:
		if c.Context != "" {
			return Message{}, fmt.Errorf("%w: an assistant message has no context", ErrInvalid)
		}
		if !p.System() {
			return Message{}, ErrForbidden
		}
	default:
		return Message{}, fmt.Errorf("%w: role %q is not user or assistant", ErrInvalid, role)
	}
	if err := c.check(); err != nil {
		return Message{}, err
	}
	m := Message{ID: s.newID("MSG-"), ConversationID: conversationID, Role: role, Text: c.Text, Actions: c.Actions,
		ProcessID: c.ProcessID, Status: c.Status, Error: c.Error, Context: c.Context, CreatedAt: s.now()}
	out, err := s.Store.Append(ctx, m, MaxMessagesPerConversation)
	return out, s.storeErr(err)
}

// UpdateMessage replaces the content of an assistant message (a platform service only): its text, actions, status
// and error; the process is set when given.
func (s *Service) UpdateMessage(ctx context.Context, p authz.Principal, id string, c Content) (Message, error) {
	if p.Anonymous() {
		return Message{}, ErrAnonymous
	}
	if !p.System() {
		return Message{}, ErrForbidden
	}
	if c.Status == "" {
		c.Status = StatusDone
	}
	if err := c.check(); err != nil {
		return Message{}, err
	}
	out, err := s.Store.UpdateMessage(ctx, id, s.now(), func(m Message) (Message, error) {
		if m.Role != RoleAssistant {
			return Message{}, fmt.Errorf("%w: only an assistant message is updated", ErrInvalid)
		}
		m.Text, m.Actions, m.Status, m.Error = c.Text, c.Actions, c.Status, c.Error
		if c.ProcessID != "" {
			m.ProcessID = c.ProcessID
		}
		return m, nil
	})
	return out, s.storeErr(err)
}

// ErrActionMissing is returned by UpdateAction when the message has no action at that index.
var ErrActionMissing = fmt.Errorf("%w: no such action", ErrInvalid)

// UpdateAction changes one action of an assistant message (a platform service only), atomically with the read: fn gets
// a copy of the action as stored and returns its replacement, or an error that leaves the message as it is. It is the
// compare-and-set the assistant needs to decide an action once (ADR 0090); the text, status and the other actions are
// untouched.
func (s *Service) UpdateAction(ctx context.Context, p authz.Principal, id string, index int, fn func(Action) (Action, error)) (Message, error) {
	if p.Anonymous() {
		return Message{}, ErrAnonymous
	}
	if !p.System() {
		return Message{}, ErrForbidden
	}
	out, err := s.Store.UpdateMessage(ctx, id, s.now(), func(m Message) (Message, error) {
		if m.Role != RoleAssistant {
			return Message{}, fmt.Errorf("%w: only an assistant message is updated", ErrInvalid)
		}
		if index < 0 || index >= len(m.Actions) {
			return Message{}, ErrActionMissing
		}
		a := make(Action, len(m.Actions[index]))
		for k, v := range m.Actions[index] {
			a[k] = v
		}
		a, err := fn(a)
		if err != nil {
			return Message{}, err
		}
		acts := append([]Action(nil), m.Actions...)
		acts[index] = a
		c := Content{Text: m.Text, Actions: acts, Status: m.Status, Error: m.Error}
		if err := c.check(); err != nil {
			return Message{}, err
		}
		m.Actions = acts
		return m, nil
	})
	return out, s.storeErr(err)
}
