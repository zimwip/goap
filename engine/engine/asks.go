package engine

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
)

// What a change waits for from people (ADR 0117): every task a run of the change waits on a person for, and the
// decision points only a person may settle, gathered in one list with whom each one is addressed to and whether the
// caller may answer it. The tasks themselves stay where they are (the pending task of a run, the decision point of
// the change): this is a read model, nothing is stored.

// Kinds of asks besides the kinds of a HumanTask (TaskInput, TaskApproval, ...).
const (
	AskClarification = "clarification" // a run waits for the answer to a question about its intent
	AskQuestion      = "question"      // a question blocks a decision point of the change
	AskDecision      = "decision"      // a decision point only a person may rule, or a ruling to ratify
)

// Ask is one thing the change waits for from a person.
type Ask struct {
	// ID is stable while the ask stands: "process:<id>:<step>:<kind>", "question:<id>", "decision:<point>".
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Title is what is asked, in a line; Detail what the methodology says about it.
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	// ProcessID is the run waiting (empty for a decision point), Agent its agent, Step the step of the process it
	// carries out and Action the action waiting.
	ProcessID string `json:"processId,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Step      string `json:"step,omitempty"`
	Action    string `json:"action,omitempty"`
	// Roles are the roles the ask is addressed to (any of them answers); Owner the person it is addressed to when no
	// role is: the initiator of the run. Both empty: anyone who may work on the change.
	Roles []string `json:"roles,omitempty"`
	Owner string   `json:"owner,omitempty"`
	// Since is when the change started waiting.
	Since time.Time `json:"since"`
	// MayAnswer tells whether the caller may answer it (the same rules as the call answering it).
	MayAnswer bool `json:"mayAnswer"`
	// Point and Question name the decision point and its question.
	Point    string `json:"point,omitempty"`
	Question string `json:"question,omitempty"`
}

// ChangeAsks lists what the change waits for from people, the oldest first, judged for the caller of ctx.
func (e *Engine) ChangeAsks(ctx context.Context, changeID domain.ChangeID) ([]Ask, error) {
	who := authz.From(ctx)
	all, err := e.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []Ask{}
	for _, p := range all {
		if p.ChangeID != changeID {
			continue
		}
		a, ok, err := e.processAsk(ctx, p, who)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, a)
		}
	}
	bb, err := e.blackboard(ctx, changeID, "")
	if err != nil {
		return nil, err
	}
	out = append(out, decisionAsks(domain.DecisionPointsOf(bb))...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out, nil
}

// processAsk is the ask of a run waiting on a person, if it waits on one.
func (e *Engine) processAsk(ctx context.Context, p *Process, who authz.Principal) (Ask, bool, error) {
	a := Ask{ProcessID: p.ID, Agent: p.Agent, Since: p.UpdatedAt}
	if p.Status == StatusClarifying {
		a.ID, a.Kind, a.Title, a.Owner = "process:"+p.ID+":clarification", AskClarification, p.Question, p.Initiator.Subject
		a.MayAnswer = true
		return a, a.Title != "", nil
	}
	t := p.Pending
	if t == nil || (p.Status != StatusWaiting && p.Status != StatusStuck) {
		return a, false, nil
	}
	switch t.Kind {
	case TaskAgent, TaskRelaunched:
		// a sub-agent, or the decision on another run's flow (that run carries the ask)
		return a, false, nil
	}
	a.ID = "process:" + p.ID + ":" + strconv.Itoa(t.Step) + ":" + t.Kind
	a.Kind, a.Action, a.Detail = t.Kind, t.Action, t.Instructions
	if t.Step >= 0 && t.Step < len(p.Steps) && !p.Steps[t.Step].StartedAt.IsZero() {
		a.Since = p.Steps[t.Step].StartedAt
	}
	sc := t.Context
	if sc == nil {
		sc = p.Step
	}
	if sc != nil {
		a.Step = sc.Path
	}
	a.Title = t.Description
	if a.Title == "" && sc != nil {
		a.Title = firstNonEmpty(sc.Description, sc.Name)
	}
	if a.Title == "" {
		a.Title = t.Action
	}
	m, err := e.methodologyOf(ctx, p)
	if err != nil {
		return a, false, err
	}
	var allowed error
	switch t.Kind {
	case TaskInput:
		act, found := m.Action(t.Action)
		if found {
			a.Roles = e.runRoles(m, p, act)
		}
		if r := t.Context.responsible(); r != "" {
			a.Roles = appendRole(a.Roles, r)
		} else if found && act.Role != "" {
			a.Roles = appendRole(a.Roles, act.Role) // the role the action acts as (ADR 0139)
		}
		allowed = e.maySubmit(ctx, p, m, who)
	case TaskApproval:
		a.Roles = slices.Clone(t.Roles)
		if t.Context != nil && t.Context.Roles != nil && t.Context.Roles.Accountable != "" {
			a.Roles = appendRole(a.Roles, t.Context.Roles.Accountable)
		}
		allowed = yes(e.mayApprove(ctx, p, who))
	case TaskCondition, TaskUnblock:
		if sc := p.Step; sc != nil && sc.Roles != nil && sc.Roles.Accountable != "" {
			a.Roles = []string{sc.Roles.Accountable}
		}
		allowed = yes(e.mayUnblock(ctx, p, who))
	}
	if len(a.Roles) == 0 {
		a.Owner = p.Initiator.EndUser()
	}
	switch {
	case allowed == nil:
		a.MayAnswer = true
	case errors.Is(allowed, authz.ErrForbidden):
	default:
		return a, false, allowed
	}
	return a, true, nil
}

// decisionAsks are the open questions of the decision points of a change and the points only a person may settle.
func decisionAsks(points []domain.DecisionPoint) []Ask {
	var out []Ask
	for _, d := range points {
		if !d.Pending() {
			continue
		}
		asked := false
		for _, q := range d.Questions {
			if q.Status != domain.QuestionOpen {
				continue
			}
			asked = true
			out = append(out, Ask{ID: "question:" + q.ID, Kind: AskQuestion, Title: q.Text, Detail: d.Question, ProcessID: q.Process,
				Since: q.AskedAt, MayAnswer: true, Point: d.ID, Question: q.ID})
		}
		if asked {
			continue
		}
		if d.Status == domain.PointRatifying || d.NeedsHuman() {
			a := Ask{ID: "decision:" + d.ID, Kind: AskDecision, Title: d.Question, Detail: d.Escalation, Since: d.OpenedAt,
				MayAnswer: true, Point: d.ID}
			if d.Ruling != nil {
				a.Since = d.Ruling.At
			}
			out = append(out, a)
		}
	}
	return out
}

// yes turns a permission check into the error of a refusal.
func yes(ok bool, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return authz.ErrForbidden
	}
	return nil
}

func appendRole(roles []string, r string) []string {
	if slices.Contains(roles, r) {
		return roles
	}
	return append(roles, r)
}
