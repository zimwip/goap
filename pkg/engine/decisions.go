package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/graph"
)

// Decision loops (ADR 0009 §4). An action works on the decision points of its change through items of kind
// "decisionPoint" (LLM output, script result, human input): open a point, rule it (decided, or undecidable with the
// questions that block it), answer a question, ratify a ruling. A ruling from a human task is a person's; from any
// other action an agent's, which waits for a ratification below the threshold of the point. The builtin
// decision.investigate answers an open question by running a sub-agent whose intent is the question.

// DecisionOp is the operation of a "decisionPoint" item.
type DecisionOp struct {
	// Op is open, rule, answer or ratify.
	Op string `json:"op"`
	// Ref names the point an open creates, for the operations of the same batch ("#ref").
	Ref string `json:"ref,omitempty"`
	// Point is the id of a point, or "#ref" of one opened in the batch; empty: the only pending point.
	Point string `json:"point,omitempty"`
	// open
	Question  string   `json:"question,omitempty"`
	Options   []string `json:"options,omitempty"` // option ids or names; none: the open options of the change
	Criteria  []string `json:"criteria,omitempty"`
	Decider   string   `json:"decider,omitempty"`
	Threshold float64  `json:"threshold,omitempty"`
	MaxRounds int      `json:"maxRounds,omitempty"`
	// MaxDuration is a Go duration ("48h") after which the point is escalated.
	MaxDuration string `json:"maxDuration,omitempty"`
	// rule
	Outcome       string   `json:"outcome,omitempty"`
	Option        string   `json:"option,omitempty"` // option id or name
	Confidence    float64  `json:"confidence,omitempty"`
	Justification string   `json:"justification,omitempty"`
	Questions     []string `json:"questions,omitempty"`
	// answer
	QuestionID string `json:"questionId,omitempty"`
	Answer     string `json:"answer,omitempty"`
	// ratify
	Accept  bool   `json:"accept,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// applyDecisionOps applies the decision operations of an action, in order, and returns the decision points they
// worked on. human tells they come from a person.
func (e *Engine) applyDecisionOps(ctx context.Context, p *Process, ops []DecisionOp, human bool, by string) ([]string, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	if p.ChangeID == "" {
		return nil, fmt.Errorf("process %s has no change attached: call goap-scheduler/attach first (ADR 0031)", p.ID)
	}
	bb, err := e.Graph.BlackboardIn(ctx, p.ChangeID, domain.MainFlow)
	if err != nil {
		return nil, err
	}
	var points []string
	touched := func(d domain.DecisionPoint) {
		if !slices.Contains(points, d.ID) {
			points = append(points, d.ID)
		}
	}
	option := func(s string) string { // an option by id or by name
		for _, o := range bb.Options {
			if o.ID == s || (o.Option != nil && strings.EqualFold(o.Option.Name, s)) {
				return o.ID
			}
		}
		return s
	}
	refs := map[string]string{}
	point := func(s string) string {
		if id, ok := refs[strings.TrimPrefix(s, "#")]; ok && s != "" {
			return id
		}
		return s
	}
	for i, op := range ops {
		fail := func(err error) ([]string, error) {
			return points, fmt.Errorf("decision operation %d (%s): %w", i, op.Op, err)
		}
		switch op.Op {
		case domain.DecisionOpenOp:
			in := graph.OpenDecisionRequest{Question: op.Question, Criteria: op.Criteria, Decider: op.Decider, Threshold: op.Threshold,
				MaxRounds: op.MaxRounds, By: by}
			for _, o := range op.Options {
				in.Options = append(in.Options, option(o))
			}
			if op.MaxDuration != "" {
				d, err := time.ParseDuration(op.MaxDuration)
				if err != nil {
					return fail(err)
				}
				in.MaxDuration = d
			}
			d, err := e.Graph.OpenDecision(ctx, p.ChangeID, in)
			if err != nil {
				return fail(err)
			}
			touched(d)
			if op.Ref != "" {
				refs[strings.TrimPrefix(op.Ref, "#")] = d.ID
			}
		case domain.DecisionRuleOp:
			in := graph.RuleRequest{Point: point(op.Point), Outcome: op.Outcome, Confidence: op.Confidence, Justification: op.Justification,
				Questions: op.Questions, Human: human, By: by}
			if op.Option != "" {
				in.Option = option(op.Option)
			}
			d, err := e.Graph.RuleDecision(ctx, p.ChangeID, in)
			if err != nil {
				return fail(err)
			}
			touched(d)
		case domain.DecisionAnswerOp:
			d, err := e.Graph.AnswerQuestion(ctx, p.ChangeID, op.QuestionID, op.Answer, "", by)
			if err != nil {
				return fail(err)
			}
			touched(d)
		case domain.DecisionRatifyOp:
			if !human {
				return fail(errors.New("a ruling is ratified by a person: a human task, not an agent"))
			}
			d, err := e.Graph.RatifyDecision(ctx, p.ChangeID, point(op.Point), op.Accept, by, op.Comment)
			if err != nil {
				return fail(err)
			}
			touched(d)
		default:
			return fail(fmt.Errorf("unknown decision operation %q: open, rule, answer or ratify", op.Op))
		}
	}
	return points, nil
}

// Investigate is the builtin decision.investigate (ADR 0009 §4): it answers the open questions of the decision
// points of the change, one sub-agent per question, whose intent is the question. Identification picks the agent,
// in every published methodology (params: methodology and agent restrict it). The action waits while a sub-agent
// runs; when it ends, its outcome answers the question, and the planner comes back to the decision.
// Params: methodology, agent, max (questions per run, default all).
func Investigate(ctx context.Context, ac ActionContext) (ActionResult, error) {
	h := ac.Host
	if h == nil || ac.Process.ChangeID == "" {
		return ActionResult{}, errors.New("decision.investigate needs a process on a change")
	}
	methodologyName, _ := ac.Action.Params["methodology"].(string)
	agent, _ := ac.Action.Params["agent"].(string)
	limit := -1
	switch v := ac.Action.Params["max"].(type) {
	case int:
		limit = v
	case float64:
		limit = int(v)
	}
	answered := 0
	for _, d := range ac.Blackboard.DecisionPoints {
		for _, q := range d.Questions {
			if q.Status != domain.QuestionOpen || (limit >= 0 && answered >= limit) {
				continue
			}
			intent := fmt.Sprintf("%s\n\n(to decide: %s)", q.Text, d.Question)
			res, err := h.e.runInvestigation(authz.With(ctx, h.e.actor(ac.Process)), h, ac.Action.Name+"#"+q.ID, methodologyName, agent, intent)
			if errors.Is(err, dsl.ErrSuspended) {
				return ActionResult{Suspended: true, Child: h.waitingOn, Output: fmt.Sprintf("investigating %q", q.Text)}, nil
			}
			answer := ""
			switch {
			case err != nil:
				answer = fmt.Sprintf("the investigation could not run: %v", err)
			case res.Status == string(StatusCompleted):
				answer = fmt.Sprintf("investigated by process %s: goal %q reached; its findings are on the blackboard of the change", res.ProcessID, res.Goal)
			default:
				answer = fmt.Sprintf("investigated by process %s: it ended %s without an answer", res.ProcessID, res.Status)
			}
			if _, err := ac.Graph.AnswerQuestion(ctx, ac.Process.ChangeID, q.ID, answer, res.ProcessID, ac.Action.Name); err != nil {
				return ActionResult{}, err
			}
			answered++
		}
	}
	return ActionResult{Output: fmt.Sprintf("%d question(s) answered", answered)}, nil
}

// runInvestigation runs (or resumes) the sub-agent of an investigation: like runChild, but identification may pick
// an agent of any methodology when methodologyName is empty (ADR 0009 §4: the multi-methodology axis).
func (e *Engine) runInvestigation(ctx context.Context, h *Host, key, methodologyName, agent, intent string) (dsl.AgentResult, error) {
	parent := h.process
	if _, started := parent.Children[key]; !started && methodologyName == "" && agent != "" {
		methodologyName = parent.Methodology // an agent is named in a methodology
	}
	return e.runChildIn(ctx, h, key, methodologyName, agent, intent, methodologyName == "")
}
