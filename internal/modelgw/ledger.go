package modelgw

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/journal"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/llmcfg"
)

// The ledger of LLM calls (ADR 0089): one row per call the gateway served or refused. The exchange (request and answer)
// of an engine call of a change stays in the change log (ADR 0059); every other call has it stored next to its row.

// Call kinds.
const (
	KindComplete = "complete"
	KindEmbed    = "embed"
)

// MaxCallError caps the error text of a call (bytes).
const MaxCallError = 500

// Call is one row of the ledger.
type Call struct {
	Seq        int64
	At         time.Time
	DurationMs int64
	// Subject, Project and Org are the principal's, never declared by the caller.
	Subject, Project, Org string
	// Alias is the name requested, "" for a literal provider/model.
	Alias, Provider, Model, Kind string
	InputTokens, OutputTokens    int64
	Error                        string
	// Source and the correlation ids are declared by the caller (llm.CallMeta).
	Source, ConversationID, ProcessID, ChangeID string
	// Step and CallIndex are llm.NoStep without a process.
	Step      int
	Action    string
	Agent     string
	CallIndex int
	// Behaviors are the global behaviours the gateway added to the instructions (ADR 0093; "!name": dropped by the cap),
	// BehaviorTokens the tokens they added (an estimate).
	Behaviors      []string
	BehaviorTokens int64
	// BehaviorTokensEstimated: BehaviorTokens is the byte estimate, not a cost measured on the model.
	BehaviorTokensEstimated bool
	// HasExchange says the gateway stores the exchange of this row (read only: set by the store, ignored on write).
	HasExchange bool
}

// Exchange is the request and the answer of a call the gateway stores (ADR 0089); Seq is the row's.
type Exchange struct {
	Seq      int64
	System   string
	Messages []llm.Message
	Response string
	// Truncated says a text was cut to journal.MaxExchangeText.
	Truncated bool
}

// capped returns the exchange with every text cut to journal.MaxExchangeText.
func (x Exchange) capped() Exchange {
	cut := func(s string) string {
		if len(s) <= journal.MaxExchangeText {
			return s
		}
		x.Truncated = true
		return strings.ToValidUTF8(s[:journal.MaxExchangeText], "")
	}
	x.System, x.Response = cut(x.System), cut(x.Response)
	msgs := make([]llm.Message, len(x.Messages))
	for i, m := range x.Messages {
		msgs[i] = llm.Message{Role: m.Role, Content: cut(m.Content)}
	}
	x.Messages = msgs
	return x
}

// inChangeLog reports whether the exchange of a call is kept by the change log (ADR 0059): an engine call of a change.
// The ledger stores the exchange of every other call.
func inChangeLog(m llm.CallMeta) bool { return m.Source == llm.SourceEngine && m.ChangeID != "" }

// UsageFilter selects calls; empty fields match everything.
type UsageFilter struct {
	From, To                               time.Time
	Subject, Project, Model, Alias, Source string
	ProcessID, ChangeID, ConversationID    string
	AfterSeq                               int64
	Limit                                  int
}

// Limits of a listing.
const (
	DefaultUsageLimit = 500
	MaxUsageLimit     = 5000
)

func (f UsageFilter) limit() int {
	switch {
	case f.Limit <= 0:
		return DefaultUsageLimit
	case f.Limit > MaxUsageLimit:
		return MaxUsageLimit
	}
	return f.Limit
}

// match reports whether a call is selected by the filter (AfterSeq and Limit aside).
func (f UsageFilter) match(c Call) bool {
	eq := func(want, got string) bool { return want == "" || want == got }
	return (f.From.IsZero() || !c.At.Before(f.From)) && (f.To.IsZero() || c.At.Before(f.To)) &&
		eq(f.Subject, c.Subject) && eq(f.Project, c.Project) && eq(f.Alias, c.Alias) && eq(f.Source, c.Source) &&
		eq(f.ProcessID, c.ProcessID) && eq(f.ChangeID, c.ChangeID) && eq(f.ConversationID, c.ConversationID) &&
		(f.Model == "" || f.Model == c.Model || f.Model == c.Provider+"/"+c.Model)
}

// Grouping of a summary.
const (
	GroupModel   = "model"
	GroupAlias   = "alias"
	GroupSource  = "source"
	GroupSubject = "subject"
	GroupProcess = "process"
	GroupAction  = "action"
	GroupAgent   = "agent"
	GroupDay     = "day"
	GroupHour    = "hour"
)

// Buckets of the time groupings (milliseconds): the key of a row is the bucket number, formatted by bucketKey.
const (
	dayMs  = 24 * 3600 * 1000
	hourMs = 3600 * 1000
)

// SummaryRow is one group of a summary.
type SummaryRow struct {
	Key                  string
	Calls, Input, Output int64
	Errors, DurationMs   int64
}

func validGroup(g string) bool {
	return slices.Contains([]string{GroupModel, GroupAlias, GroupSource, GroupSubject, GroupProcess, GroupAction, GroupAgent, GroupDay, GroupHour}, g)
}

func bucketKey(group string, n int64) string {
	if group == GroupDay {
		return time.UnixMilli(n * dayMs).UTC().Format("2006-01-02")
	}
	return time.UnixMilli(n * hourMs).UTC().Format("2006-01-02T15")
}

func groupKey(c Call, group string) string {
	switch group {
	case GroupModel:
		return c.Model
	case GroupAlias:
		return c.Alias
	case GroupSource:
		return c.Source
	case GroupSubject:
		return c.Subject
	case GroupProcess:
		return c.ProcessID
	case GroupAction:
		return c.Action
	case GroupAgent:
		return c.Agent
	case GroupDay:
		return bucketKey(group, c.At.UnixMilli()/dayMs)
	}
	return bucketKey(GroupHour, c.At.UnixMilli()/hourMs)
}

// summarize groups calls in memory (the memory store).
func summarize(calls []Call, group string) []SummaryRow {
	by := map[string]*SummaryRow{}
	for _, c := range calls {
		k := groupKey(c, group)
		r := by[k]
		if r == nil {
			r = &SummaryRow{Key: k}
			by[k] = r
		}
		r.Calls++
		r.Input += c.InputTokens
		r.Output += c.OutputTokens
		r.DurationMs += c.DurationMs
		if c.Error != "" {
			r.Errors++
		}
	}
	out := make([]SummaryRow, 0, len(by))
	for _, r := range by {
		out = append(out, *r)
	}
	sortSummary(out, group)
	return out
}

// sortSummary orders a summary: time groups ascending, the others by decreasing tokens.
func sortSummary(rows []SummaryRow, group string) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if group == GroupDay || group == GroupHour {
			return a.Key < b.Key
		}
		if ta, tb := a.Input+a.Output, b.Input+b.Output; ta != tb {
			return ta > tb
		}
		return a.Key < b.Key
	})
}

// ---- recording --------------------------------------------------------------

// pending is a call being made: what is known when it starts, completed by finish.
type pending struct {
	s     *Service
	call  Call
	start time.Time
	// x is the exchange to store, nil when the call's is kept elsewhere or the storage is off.
	x *Exchange
}

// begin opens the ledger row of a call: the principal is the context's, the declaration the meta of the context.
func (s *Service) begin(ctx context.Context, kind, requested string) *pending {
	p := authz.From(ctx)
	m := llm.MetaFrom(ctx)
	alias := requested
	if alias == "" {
		alias = "default"
		if kind == KindEmbed {
			alias = llm.EmbedAlias
		}
	}
	if strings.Contains(alias, "/") {
		alias = "" // a literal provider/model
	}
	now := s.now()
	p0 := &pending{s: s, start: now, call: Call{At: now.UTC(), Subject: p.Subject, Project: p.Project, Org: p.Org, Alias: alias, Kind: kind,
		Source: m.Source, ConversationID: m.ConversationID, ProcessID: m.ProcessID, ChangeID: m.ChangeID, Step: m.Step, Action: m.Action, Agent: m.Agent, CallIndex: m.Call}}
	if s.CallPrompts && !inChangeLog(m) {
		p0.x = &Exchange{}
	}
	return p0
}

// request notes what is sent, for the exchange.
func (p *pending) request(system string, msgs []llm.Message) {
	if p.x != nil {
		p.x.System, p.x.Messages = system, msgs
	}
}

// behaved notes the behaviours added to the instructions of the call.
func (p *pending) behaved(ap llmcfg.Applied, tokens int, estimated bool) {
	p.call.Behaviors, p.call.BehaviorTokens, p.call.BehaviorTokensEstimated = behaviorNames(ap), int64(tokens), estimated
}

// answer notes what came back, for the exchange.
func (p *pending) answer(text string) {
	if p.x != nil {
		p.x.Response = text
	}
}

// maxPreviewTexts and maxPreviewText bound what the exchange of an embedding keeps of its texts.
const (
	maxPreviewTexts = 5
	maxPreviewText  = 300
)

// requestEmbed notes the texts of an embedding: their number and a short preview (never the vectors).
func (p *pending) requestEmbed(texts []string) {
	if p.x == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d texts", len(texts))
	for i, t := range texts {
		if i == maxPreviewTexts {
			fmt.Fprintf(&b, "\n... %d more", len(texts)-i)
			break
		}
		if len(t) > maxPreviewText {
			t = strings.ToValidUTF8(t[:maxPreviewText], "") + "..."
		}
		b.WriteString("\n- " + t)
	}
	p.x.Messages = []llm.Message{{Role: "texts", Content: b.String()}}
}

// finish writes the row: the provider and model that served (or were resolved), the tokens and the error. It never
// fails the call; a row that cannot be written is logged like the quota counter.
func (p *pending) finish(ctx context.Context, provider, model string, in, out int, err error) {
	c := p.call
	c.Provider, c.Model, c.InputTokens, c.OutputTokens = provider, model, int64(in), int64(out)
	c.DurationMs = max(p.s.now().Sub(p.start).Milliseconds(), 0)
	if err != nil {
		c.Error = err.Error()
		if len(c.Error) > MaxCallError {
			c.Error = strings.ToValidUTF8(c.Error[:MaxCallError], "")
		}
	}
	var x *Exchange
	if p.x != nil {
		cx := p.x.capped()
		x = &cx
	}
	if _, e := p.s.Store.AppendCall(context.WithoutCancel(ctx), c, x); e != nil {
		p.s.Log.Error("call not recorded", "source", c.Source, "model", provider+"/"+model, "err", e)
	}
}

// ---- reading ----------------------------------------------------------------

// scope applies who may read what: a caller reads its own calls (the filter's subject is forced to it, naming another
// is refused); an administrator any. A caller without identity reads nothing.
func scope(ctx context.Context, f UsageFilter, admin bool) (UsageFilter, error) {
	p := authz.From(ctx)
	if admin {
		return f, nil
	}
	if p.Anonymous() {
		return f, fmt.Errorf("%w: the ledger is read by an identified caller", ErrForbidden)
	}
	if f.Subject != "" && f.Subject != p.Subject {
		return f, fmt.Errorf("%w: only the calls of %s are readable by it", ErrForbidden, p.Subject)
	}
	f.Subject = p.Subject
	return f, nil
}

// ListCalls lists the calls of the filter, ascending by seq: with AfterSeq the first ones after it, else the latest
// Limit ones. next is the cursor to resume from; more says matching calls remain beyond the returned ones. admin: the
// caller may read the calls of any subject.
func (s *Service) ListCalls(ctx context.Context, f UsageFilter, admin bool) (calls []Call, next int64, more bool, err error) {
	if f, err = scope(ctx, f, admin); err != nil {
		return nil, 0, false, err
	}
	calls, more, err = s.Store.Calls(ctx, f)
	if err != nil {
		return nil, 0, false, err
	}
	next = f.AfterSeq
	if n := len(calls); n > 0 {
		next = calls[n-1].Seq
	}
	return calls, next, more, nil
}

// CallExchange returns a call and the exchange the gateway stores for it. A caller reads its own calls, an
// administrator any; a call of someone else is ErrNotFound for the others (its existence is not told), as is a call
// with no stored exchange (kept by a change log, not stored, or purged).
func (s *Service) CallExchange(ctx context.Context, seq int64, admin bool) (Call, Exchange, error) {
	p := authz.From(ctx)
	if !admin && p.Anonymous() {
		return Call{}, Exchange{}, fmt.Errorf("%w: the ledger is read by an identified caller", ErrForbidden)
	}
	c, err := s.Store.Call(ctx, seq)
	if err != nil {
		return Call{}, Exchange{}, err
	}
	if !admin && c.Subject != p.Subject {
		return Call{}, Exchange{}, fmt.Errorf("%w: call %d", ErrNotFound, seq)
	}
	x, err := s.Store.Exchange(ctx, seq)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Call{}, Exchange{}, err
	}
	if err != nil {
		return Call{}, Exchange{}, fmt.Errorf("%w: no exchange stored for call %d (not kept, or purged)", ErrNotFound, seq)
	}
	return c, x, nil
}

// Summary groups the calls of the filter (AfterSeq and Limit are ignored).
func (s *Service) Summary(ctx context.Context, f UsageFilter, group string, admin bool) ([]SummaryRow, error) {
	if !validGroup(group) {
		return nil, fmt.Errorf("%w: group_by %q (model, alias, source, subject, process, action, agent, day or hour)", ErrInvalid, group)
	}
	f, err := scope(ctx, f, admin)
	if err != nil {
		return nil, err
	}
	f.AfterSeq, f.Limit = 0, 0
	return s.Store.Summary(ctx, f, group)
}

// ---- retention --------------------------------------------------------------

// DefaultCallRetentionDays is how long the ledger keeps a call (GOAP_LLM_CALL_RETENTION_DAYS); 0 keeps them forever.
// The quota counter is not the ledger and is not purged.
const DefaultCallRetentionDays = 90

// PurgeCalls deletes the calls older than days days; it returns how many. days <= 0 deletes nothing.
func (s *Service) PurgeCalls(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	return s.Store.PurgeCalls(ctx, s.now().AddDate(0, 0, -days))
}

// KeepCalls purges the ledger now and then every day until ctx ends. Run it in a goroutine.
func (s *Service) KeepCalls(ctx context.Context, days int) {
	if days <= 0 {
		return
	}
	for {
		if n, err := s.PurgeCalls(ctx, days); err != nil {
			s.Log.Error("ledger purge", "err", err)
		} else if n > 0 {
			s.Log.Info("ledger purge", "deleted", n, "olderThanDays", days)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}
