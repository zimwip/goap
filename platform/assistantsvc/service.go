// Package assistantsvc is the conversational assistant (ADR 0087): it answers the messages a person writes in a
// conversation (ADR 0085) with the model of the `assistant` alias, and may do a few things for them through its server
// tools, no more (ADR 0090 added the agents), and asks the interface to act on the screen they are on (ADR 0092: its
// tools are handed back, never run here). Each turn follows its frame (frame.go): the active project, the change the
// person is looking at and its methodology, computed from the context and the history; the tools offered and the
// prompt are those of the frame, and a change is created only once the open changes were searched (search_changes).
// It is a use case over the graph, the model gateway and the conversation service: `pkg/graph` and `pkg/engine` do
// not import it (`pkg/layering`).
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

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/llm"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/llmcfg"
	"github.com/zimwip/goap/platform/convsvc"
	"github.com/zimwip/goap/platform/modelgw"
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
	// errStopped is the cause of the context of a turn the person stopped (Cancel).
	errStopped = errors.New("the person stopped the answer")
)

// Alias is the model alias that serves the assistant (a protected alias, ADR 0084).
const Alias = llmcfg.AssistantAlias

// Limits.
const (
	// MaxRounds is the number of model calls of a turn: each may ask for tools, whose results go back to the model
	// in the next call; the last one is offered no tool and must answer with what it has. The person can stop a turn
	// at any time (Cancel).
	MaxRounds = 16
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
	TurnTimeout = 5 * time.Minute
	StaleAfter  = 7 * time.Minute

	maxAnswerBytes  = 8 << 10
	maxToolResult   = 8 << 10
	maxMethodologys = 30
	maxProjects     = 50
	maxErrorText    = 500
)

// Model is what the assistant needs of the gateway: the model call under the catalog policy and the aliases the
// caller may use (*modelgw.Service, *modelgw.Client).
type Model = modelgw.SuggestModel

// Graph is what the assistant needs of the graph: to create a change, to read one and to list them, as the caller
// (*graph.Graph, *graphclient.Client, engine.EventingGraph).
type Graph interface {
	CreateChange(ctx context.Context, in graph.NewChange) (domain.Change, error)
	Change(ctx context.Context, id domain.ChangeID) (domain.Change, error)
	ListChanges(ctx context.Context, f graph.ChangesFilter) ([]domain.Change, error)
	// AttachChange and DetachChange carry out the attach / detach proposals the person accepted (ADR 0106).
	AttachChange(ctx context.Context, id, parent domain.ChangeID) (domain.Change, error)
	DetachChange(ctx context.Context, id domain.ChangeID) (domain.Change, error)
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
	running map[string]*liveTurn // conversation ids with a turn in this process
}

// liveTurn is a turn of this process: its context is cancelled when the person stops it.
type liveTurn struct {
	cancel  context.CancelCauseFunc // nil until the turn has started
	stopped bool                    // stopped before it started
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
	if s.running[id] != nil {
		return false
	}
	if s.running == nil {
		s.running = map[string]*liveTurn{}
	}
	s.running[id] = &liveTurn{}
	return true
}

// started records the cancel function of the turn of a conversation; a turn stopped before it started is cancelled
// at once.
func (s *Service) started(id string, cancel context.CancelCauseFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.running[id]; r != nil {
		r.cancel = cancel
		if r.stopped {
			cancel(errStopped)
		}
	}
}

// stop cancels the turn of a conversation running in this process; it reports whether there was one.
func (s *Service) stop(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.running[id]
	if r == nil {
		return false
	}
	r.stopped = true
	if r.cancel != nil {
		r.cancel(errStopped)
	}
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
	history, from := historyOf(msgs)
	summary, err := s.Convs.Summary(ctx, Principal, in.ConversationID)
	if err != nil {
		return user, pending, err
	}
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
	t := &turn{s: s, p: p, conversation: in.ConversationID, message: pending.ID, in: in, history: history, project: p.Project,
		uiTools: toolIndex(in.UITools), summary: summary, older: compactable(msgs[:from], summary.UpTo), searched: searchedIn(msgs)}
	run := func() {
		tctx := authz.With(context.WithoutCancel(ctx), p)
		rctx, cancel := context.WithCancelCause(tctx)
		s.started(in.ConversationID, cancel)
		t.run(rctx)
		cancel(nil)
		s.release(in.ConversationID) // the next message need not wait for the summary
		t.compact(tctx)
	}
	started = true
	if s.Go != nil {
		s.Go(run)
	} else {
		go run()
	}
	return user, pending, nil
}

// Cancel stops the answer being written in a conversation of the caller: the pending assistant message becomes
// cancelled at once (with what it holds) and the turn, in this process or in another one (it reads the message between
// its rounds), stops before its next model call; tools already run stay done, proposals already made stay to decide.
// With no answer pending it returns the last message as it is.
func (s *Service) Cancel(ctx context.Context, conversationID string) (convsvc.Message, error) {
	p := authz.From(ctx)
	if p.Anonymous() {
		return convsvc.Message{}, convsvc.ErrAnonymous
	}
	if p.System() {
		return convsvc.Message{}, convsvc.ErrForbidden
	}
	_, msgs, err := s.Convs.Get(ctx, p, conversationID) // the owner only
	if err != nil {
		return convsvc.Message{}, err
	}
	if len(msgs) == 0 {
		return convsvc.Message{}, fmt.Errorf("%w: the conversation has no message", ErrInvalid)
	}
	last := msgs[len(msgs)-1]
	if last.Role != convsvc.RoleAssistant || last.Status != convsvc.StatusPending {
		return last, nil
	}
	wctx := authz.With(context.WithoutCancel(ctx), Principal)
	m, err := s.Convs.UpdateMessage(wctx, Principal, last.ID, convsvc.Content{Text: last.Text, Actions: last.Actions, Status: convsvc.StatusCancelled})
	if err != nil && !errors.Is(err, convsvc.ErrCancelled) {
		return convsvc.Message{}, err
	}
	s.stop(conversationID)
	return m, nil
}

// historyOf is what is sent of the conversation before the new message: its last messages that fit the caps, oldest
// first, the pending and the empty ones left out. from is the index in msgs of the oldest message the window holds
// (len(msgs) when none): the ones before are the summary's (compact.go).
func historyOf(msgs []convsvc.Message) (out []llm.Message, from int) {
	size := 0
	var at []int // the index in msgs of each message of out
	for i := len(msgs) - 1; i >= 0 && len(out) < MaxHistoryMessages-1; i-- {
		m := msgs[i]
		text := strings.TrimSpace(m.Text)
		if m.Status == convsvc.StatusCancelled && text == "" {
			text = "[the person stopped this answer]"
		}
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
		at = append(at, i)
	}
	// the window starts with a person's message (providers want a user turn first): an assistant message at its start
	// is left to the summary rather than dropped
	for len(out) > 0 && out[len(out)-1].Role != convsvc.RoleUser {
		out, at = out[:len(out)-1], at[:len(at)-1]
	}
	from = len(msgs)
	if len(at) > 0 {
		from = at[len(at)-1]
	}
	slices.Reverse(out)
	return out, from
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
