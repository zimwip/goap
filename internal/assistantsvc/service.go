// Package assistantsvc is the conversational assistant (ADR 0087): it answers the messages a person writes in a
// conversation (ADR 0085) with the model of the `assistant` alias, and may do four things for them through tools, no
// more. It is a use case over the graph, the model gateway and the conversation service: `pkg/graph` and `pkg/engine`
// do not import it (`pkg/layering`).
package assistantsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/llmcfg"
	"github.com/zimwip/goap/pkg/methodology"
)

// Errors of the Service; the errors of the conversation service (convsvc.Err*) pass through.
var (
	// ErrInvalid is returned for a request the service does not accept (malformed, or beyond a limit).
	ErrInvalid = errors.New("invalid")
	// ErrForbidden is returned when the caller may not do that (a project they cannot work on).
	ErrForbidden = errors.New("forbidden")
	// ErrUnavailable is returned when the `assistant` model alias cannot be used by the caller.
	ErrUnavailable = errors.New("the assistant is not available")
	// ErrBusy is returned when the assistant is still answering the previous message of the conversation.
	ErrBusy = errors.New("the assistant is still answering the previous message")
)

// Alias is the model alias that serves the assistant (a protected alias, ADR 0084).
const Alias = llmcfg.AssistantAlias

// Limits.
const (
	// MaxRounds is the number of model calls of a turn: each may ask for tools, whose results go back to the model
	// in the next call; tools asked for in the last one are not run.
	MaxRounds = 4
	// MaxToolCalls caps the tools run in one round; the others get an error as their result.
	MaxToolCalls = 4
	// MaxTextBytes caps a user message.
	MaxTextBytes = 4 << 10
	// MaxContextBytes caps the context snapshot of a request, MaxSelectionBytes its selected text.
	MaxContextBytes   = 16 << 10
	MaxSelectionBytes = 2000
	// MaxHistoryMessages and MaxHistoryBytes cap what is sent of the conversation: its last messages, newest first,
	// while they fit.
	MaxHistoryMessages = 12
	MaxHistoryBytes    = 16 << 10
	// TurnTimeout bounds one turn; a pending message older than StaleAfter belongs to a turn that died with its
	// process and no longer blocks the conversation.
	TurnTimeout = 90 * time.Second
	StaleAfter  = 3 * time.Minute

	maxAnswerBytes  = 8 << 10
	maxToolResult   = 8 << 10
	maxMethodologys = 30
	maxErrorText    = 500
)

// Model is what the assistant needs of the gateway: the model call under the catalog policy and the aliases the
// caller may use (*modelgw.Service, *modelgw.Client).
type Model = modelgw.SuggestModel

// Graph is what the assistant needs of the graph: to create a change and to read one, as the caller
// (*graph.Graph, *graphsvc.Client).
type Graph interface {
	CreateChange(ctx context.Context, in graph.NewChange) (domain.Change, error)
	Change(ctx context.Context, id domain.ChangeID) (domain.Change, error)
}

// Methodologies serves a methodology by name (*registrysvc.Service, *registrysvc.Client).
type Methodologies interface {
	Methodology(ctx context.Context, name string) (*methodology.Compiled, error)
}

// Projects is what the assistant asks of the organisation: which projects exist, whether the caller may work on one
// and which methodologies apply to it (Directory).
type Projects interface {
	HasProject(ctx context.Context, project string) (bool, error)
	MayAccessProject(ctx context.Context, p authz.Principal, project string) (bool, error)
	ApplicableMethodologies(ctx context.Context, project string) ([]string, error)
}

// Directory answers Projects from the snapshot of the organisation the access directory keeps.
type Directory struct{ *access.Directory }

var _ Projects = Directory{}

func (d Directory) HasProject(ctx context.Context, project string) (bool, error) {
	s, err := d.Snapshot(ctx)
	if err != nil || s == nil {
		return false, err
	}
	return s.HasProject(project), nil
}

func (d Directory) ApplicableMethodologies(ctx context.Context, project string) ([]string, error) {
	s, err := d.Snapshot(ctx)
	if err != nil || s == nil {
		return nil, err
	}
	return s.ApplicableMethodologies(project), nil
}

// Context is the snapshot of what the person was looking at when they wrote a message: the web collects it, it is
// sent with the message, used for that turn only and never stored.
type Context struct {
	TabKind   string
	TabParams map[string]string
	// Subject is the node or change the tab is about (opaque).
	Subject   string
	Selection string
	// Project is the active project key.
	Project string
}

func (c Context) validate() error {
	if len(c.Selection) > MaxSelectionBytes {
		return fmt.Errorf("%w: the selection is %d bytes, at most %d", ErrInvalid, len(c.Selection), MaxSelectionBytes)
	}
	size := len(c.TabKind) + len(c.Subject) + len(c.Selection) + len(c.Project)
	for k, v := range c.TabParams {
		size += len(k) + len(v)
	}
	if size > MaxContextBytes {
		return fmt.Errorf("%w: the context is %d bytes, at most %d", ErrInvalid, size, MaxContextBytes)
	}
	return nil
}

// describe is the short description of the context kept on the user message: what kind of page, about what, in which
// project, and that a selection existed. Never the page, the selection or any form content.
func (c Context) describe() string {
	var parts []string
	if c.TabKind != "" {
		parts = append(parts, "tab "+clip(c.TabKind, 40))
	}
	if c.Subject != "" {
		parts = append(parts, "about "+clip(c.Subject, 80))
	}
	if c.Project != "" {
		parts = append(parts, "project "+clip(c.Project, 60))
	}
	if c.Selection != "" {
		parts = append(parts, fmt.Sprintf("%d selected characters", len([]rune(c.Selection))))
	}
	return clip(strings.Join(parts, ", "), convsvc.MaxContextBytes)
}

// Service answers the messages of the conversations.
//
// A turn is the pending assistant message itself: Send appends the user message and a pending assistant message, and
// returns; the turn then runs in the background as the caller (model call, tools) and writes the answer into that
// message as the platform service `system:assistant`. There is no engine process for it (the engine's llm action is
// a one-shot protocol and cannot chat): the message, which names its conversation, is the record of the run.
type Service struct {
	Convs         *convsvc.Service
	Model         Model
	Graph         Graph
	Methodologies Methodologies
	Projects      Projects
	Log           *slog.Logger
	// Now is the clock (time.Now when nil); Go starts the background turn (a goroutine when nil; tests run it inline).
	Now func() time.Time
	Go  func(func())

	mu      sync.Mutex
	running map[string]bool // conversation ids with a turn in this process
}

// Principal is the identity under which the answers are written (ADR 0085: only a platform service writes the words
// of the assistant).
var Principal = authz.System("assistant")

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// SendInput is a user message with the context of the page it was written on.
type SendInput struct {
	ConversationID string
	Text           string
	Context        Context
}

func (s *Service) acquire(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	if s.running == nil {
		s.running = map[string]bool{}
	}
	s.running[id] = true
	return true
}

func (s *Service) release(id string) {
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

// Send appends the user message of the caller to their conversation and a pending assistant message, starts the turn
// and returns both messages at once. It is refused when the caller has no `assistant` model alias
// (ErrUnavailable), when the previous message is still being answered (ErrBusy) and when the active project is one the
// caller may not work on (ErrForbidden).
func (s *Service) Send(ctx context.Context, in SendInput) (user, pending convsvc.Message, err error) {
	p := authz.From(ctx)
	if p.Anonymous() {
		return user, pending, convsvc.ErrAnonymous
	}
	if p.System() {
		return user, pending, convsvc.ErrForbidden
	}
	if strings.TrimSpace(in.Text) == "" || len(in.Text) > MaxTextBytes {
		return user, pending, fmt.Errorf("%w: a message needs a text of at most %d bytes", ErrInvalid, MaxTextBytes)
	}
	if err := in.Context.validate(); err != nil {
		return user, pending, err
	}
	_, aliases, err := s.Model.Available(ctx)
	if err != nil {
		return user, pending, err
	}
	if !slices.ContainsFunc(aliases, func(a modelgw.AliasEntry) bool { return a.Alias == Alias }) {
		return user, pending, fmt.Errorf("%w: the %q model alias is not available to you", ErrUnavailable, Alias)
	}
	if project := in.Context.Project; project != "" && project != p.Project {
		if ok, err := s.Projects.MayAccessProject(ctx, p, project); err != nil || !ok {
			return user, pending, errors.Join(ErrForbidden, err)
		}
	}
	if !s.acquire(in.ConversationID) {
		return user, pending, ErrBusy
	}
	started := false
	defer func() {
		if !started {
			s.release(in.ConversationID)
		}
	}()

	_, msgs, err := s.Convs.Get(ctx, p, in.ConversationID)
	if err != nil {
		return user, pending, err
	}
	if n := len(msgs); n > 0 {
		if last := msgs[n-1]; last.Role == convsvc.RoleAssistant && last.Status == convsvc.StatusPending && s.now().Sub(last.CreatedAt) < StaleAfter {
			return user, pending, ErrBusy
		}
	}
	history := historyOf(msgs)
	user, err = s.Convs.Append(ctx, p, in.ConversationID, convsvc.RoleUser, convsvc.Content{Text: in.Text, Context: in.Context.describe()})
	if err != nil {
		return user, pending, err
	}
	pending, err = s.Convs.Append(ctx, Principal, in.ConversationID, convsvc.RoleAssistant, convsvc.Content{Status: convsvc.StatusPending})
	if err != nil {
		return user, pending, err
	}
	// the turn runs as the caller, whose context ends with the request: it gets its own, with the same values
	if in.Context.Project != "" {
		p.Project = in.Context.Project
	}
	t := &turn{s: s, p: p, conversation: in.ConversationID, message: pending.ID, in: in, history: history, project: p.Project}
	run := func() {
		defer s.release(in.ConversationID)
		t.run(authz.With(context.WithoutCancel(ctx), p))
	}
	started = true
	if s.Go != nil {
		s.Go(run)
	} else {
		go run()
	}
	return user, pending, nil
}

// historyOf is what is sent of the conversation before the new message: its last messages that fit the caps, oldest
// first, the pending and the empty ones left out.
func historyOf(msgs []convsvc.Message) []llm.Message {
	var out []llm.Message
	size := 0
	for i := len(msgs) - 1; i >= 0 && len(out) < MaxHistoryMessages-1; i-- {
		m := msgs[i]
		text := strings.TrimSpace(m.Text)
		if m.Status == convsvc.StatusPending || text == "" {
			continue
		}
		text = clip(text, MaxTextBytes)
		if m.Role == convsvc.RoleAssistant {
			if acts := actionTypes(m.Actions); acts != "" {
				text += "\n(actions run for the person: " + acts + ")"
			}
		}
		if size += len(text); size > MaxHistoryBytes {
			break
		}
		out = append(out, llm.Message{Role: m.Role, Content: text})
	}
	slices.Reverse(out)
	return out
}

func actionTypes(actions []convsvc.Action) string {
	var out []string
	for _, a := range actions {
		if t, _ := a["type"].(string); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, ", ")
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
