// Package assistantsvc is the conversational assistant (ADR 0087): it answers the messages a person writes in a
// conversation (ADR 0085) with the model of the `assistant` alias, and may do seven things for them through tools, no
// more (ADR 0090 added the agents), and asks the interface to act on the screen they are on (ADR 0092: its tools are
// handed back, never run here). It is a use case over the graph, the model gateway and the conversation service: `pkg/graph` and `pkg/engine`
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
	// MaxContextBytes caps the context snapshot of a request once rendered (context.go), MaxSelectionBytes the
	// selected text.
	MaxContextBytes   = 8 << 10
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

// Graph is what the assistant needs of the graph: to create a change and to read one, and to record the request a
// change is created for (ADR 0098), as the caller (*graph.Graph, *graphsvc.Client).
type Graph interface {
	CreateChange(ctx context.Context, in graph.NewChange) (domain.Change, error)
	Change(ctx context.Context, id domain.ChangeID) (domain.Change, error)
	CreateRequest(ctx context.Context, in graph.NewRequest) (domain.Request, error)
	LinkRequest(ctx context.Context, id domain.RequestID, change domain.ChangeID, role domain.LinkRole) (domain.Request, error)
	// Objects reads change objects of a change: the state of the change in the lifecycle of its methodology is an
	// execution@State object (ADR 0098).
	Objects(ctx context.Context, id domain.ChangeID, f domain.ObjectFilter) ([]domain.ChangeObject, error)
}

// requestedChange records the request of the person (ADR 0098: the words they said, from the conversation), creates
// the change for it and links them (origin). The request stays when the change cannot be created.
func requestedChange(ctx context.Context, g Graph, words, conversation string, in graph.NewChange) (domain.Change, domain.Request, error) {
	text := words
	if text == "" {
		text = in.Intent
	}
	req, err := g.CreateRequest(ctx, graph.NewRequest{Title: in.Title, Text: text, Requester: authz.From(ctx).Subject, ProjectID: in.ProjectID,
		Origin: domain.RequestOrigin{Kind: domain.OriginConversation, Ref: conversation}})
	if err != nil {
		return domain.Change{}, req, fmt.Errorf("the request was not recorded: %w", err)
	}
	ch, err := g.CreateChange(ctx, in)
	if err != nil {
		return ch, req, err
	}
	if req, err = g.LinkRequest(ctx, req.ID, ch.ID, domain.LinkOrigin); err != nil {
		return ch, req, fmt.Errorf("the change was created but not linked to its request: %w", err)
	}
	return ch, req, nil
}

// Methodologies serves a methodology by name (*registrysvc.Service, *registrysvc.Client).
type Methodologies interface {
	Methodology(ctx context.Context, name string) (*methodology.Compiled, error)
}

// Projects is what the assistant asks of the organisation: which projects exist, whether the caller may work on one
// and which methodologies apply to it (Directory).
type Projects interface {
	HasProject(ctx context.Context, project string) (bool, error)
	// RootProject is the project of a caller with no active project (ADR 0091).
	RootProject(ctx context.Context) (string, error)
	MayAccessProject(ctx context.Context, p authz.Principal, project string) (bool, error)
	ApplicableMethodologies(ctx context.Context, project string) ([]string, error)
}

// Directory answers Projects from the snapshot of the organisation the access directory keeps.
type Directory struct{ *access.Directory }

var _ Projects = Directory{}

func (d Directory) RootProject(ctx context.Context) (string, error) {
	s, err := d.Snapshot(ctx)
	if err != nil || s == nil {
		return "", fmt.Errorf("the organisation cannot be read: %w", err)
	}
	return s.RootProject(), nil
}

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
	// Engine runs the agents the person confirms (ADR 0090); nil: the agent tools refuse.
	Engine Engine
	Log    *slog.Logger
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

// timed logs, at debug level, the duration of a phase of the assistant (GOAP_LOG_LEVEL=debug): the measure of where a
// turn spends its time. Use as `defer s.timed(ctx, "phase", time.Now())`.
func (s *Service) timed(ctx context.Context, phase string, start time.Time, kv ...any) {
	if l := s.log(); l.Enabled(ctx, slog.LevelDebug) {
		l.Debug("assistant phase", append([]any{"phase", phase, "duration_ms", float64(time.Since(start).Microseconds()) / 1000}, kv...)...)
	}
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
	// UITools are the tools the current screen offers this turn (ADR 0092); the service never runs them.
	UITools []UITool
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
	defer s.timed(ctx, "send", time.Now())
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
	if in.Context, err = in.Context.normalise(); err != nil {
		return user, pending, err
	}
	if in.UITools, err = checkTools(in.UITools); err != nil {
		return user, pending, err
	}
	t0 := time.Now()
	_, aliases, err := s.Model.Available(ctx)
	s.timed(ctx, "send.available", t0)
	if err != nil {
		return user, pending, err
	}
	if !slices.ContainsFunc(aliases, func(a modelgw.AliasEntry) bool { return a.Alias == Alias }) {
		return user, pending, fmt.Errorf("%w: the %q model alias is not available to you", ErrUnavailable, Alias)
	}
	if project := in.Context.App.Project; project != "" && project != p.Project {
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

	t0 = time.Now()
	_, msgs, err := s.Convs.Get(ctx, p, in.ConversationID)
	s.timed(ctx, "send.history", t0, "messages", len(msgs))
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
	if in.Context.App.Project != "" {
		p.Project = in.Context.App.Project
	}
	t := &turn{s: s, p: p, conversation: in.ConversationID, message: pending.ID, in: in, history: history, project: p.Project, uiTools: toolIndex(in.UITools)}
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
			if acts := describeActions(m.Actions); acts != "" {
				text += "\n" + acts
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

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
