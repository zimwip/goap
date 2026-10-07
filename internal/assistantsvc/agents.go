package assistantsvc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
)

// Running agents (ADR 0090). The assistant never starts one by itself: start_agent records a proposal as an action of
// its message, and only the person's ConfirmAction starts it, as them.

// ToolListAgents and ToolStartAgent are the two tools of the agents.
const (
	ToolListAgents = "list_agents"
	ToolStartAgent = "start_agent"
)

// Action types, statuses and keys of the proposal.
const (
	ActionStartAgent = "start_agent"

	// StatusProposed waits for the person; StatusStarting is the claim of an accept in progress; the others are
	// final.
	StatusProposed = "proposed"
	StatusStarting = "starting"
	StatusStarted  = "started"
	StatusRejected = "rejected"
	StatusFailed   = "failed"

	DecisionAccept = "accept"
	DecisionReject = "reject"

	maxAgents       = 30
	maxAgentGoals   = 5
	maxRunningShown = 10
)

// Errors of the confirmation.
var (
	// ErrDecided is returned when the action was already decided.
	ErrDecided = errors.New("this action was already decided")
	// ErrStale is returned when a proposal no longer holds: the active project is not its project, the change cannot be
	// worked on any more, the agent is no longer applicable or runnable by the caller. It stays proposed.
	ErrStale = errors.New("the proposal is stale")
)

// agentEntry is an agent the caller may run here.
type agentEntry struct {
	Methodology string      `json:"methodology"`
	Agent       string      `json:"agent"`
	Description string      `json:"description,omitempty"`
	Examples    []string    `json:"examples,omitempty"`
	Goals       []goalEntry `json:"goals,omitempty"`
	Roles       []string    `json:"requiredRoles,omitempty"`
	NeedsChange bool        `json:"needsChange"`
	Running     bool        `json:"running"`
	goals       []string    // every goal name of the agent, whatever the cap of Goals
}

type goalEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// changeView is the change of the context, as the caller reads it.
type changeView struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Methodology string        `json:"methodology,omitempty"`
	Namespace   string        `json:"namespace,omitempty"`
	Project     string        `json:"project,omitempty"`
	Status      string        `json:"status"`
	Lifecycle   string        `json:"lifecycle,omitempty"`
	State       string        `json:"state,omitempty"`
	Processes   []processView `json:"runningProcesses,omitempty"`
}

type processView struct {
	ID     string `json:"id"`
	Agent  string `json:"agent"`
	Status string `json:"status"`
}

// candidates is what the caller may run here: the agents after the three filters (the change of the context, or its
// absence; the methodologies applicable to the project; the roles of the caller on it).
type candidates struct {
	project string
	change  *changeView
	note    string
	agents  []agentEntry
}

func (c candidates) find(methodologyName, agent string) (agentEntry, bool) {
	for _, a := range c.agents {
		if a.Methodology == methodologyName && a.Agent == agent {
			return a, true
		}
	}
	return agentEntry{}, false
}

// startable reports whether a change in that status can still take a process.
func startable(s domain.ChangeStatus) bool {
	return s == domain.ChangeDraft || s == domain.ChangeActive
}

// readChange reads a change as the caller; one that does not exist or is a personal change of someone else is the
// same error.
func (s *Service) readChange(ctx context.Context, p authz.Principal, id string) (domain.Change, error) {
	ch, err := s.Graph.Change(authz.With(ctx, p), domain.ChangeID(id))
	if err != nil || (access.IsPersonal(ch) && !access.IsPersonalTo(ch, p.Subject)) {
		return domain.Change{}, fmt.Errorf("change %q does not exist or is not visible to you", id)
	}
	return ch, nil
}

// runnable computes the agents the caller p may run on the active project, in the context of a change ("" for none).
func (s *Service) runnable(ctx context.Context, p authz.Principal, project, changeID string) (candidates, error) {
	if s.Engine == nil {
		return candidates{}, errors.New("agents cannot be run from here: the engine is not reachable")
	}
	c := candidates{project: project}
	var names []string
	if changeID != "" {
		ch, err := s.readChange(ctx, p, changeID)
		if err != nil {
			return c, err
		}
		if ch.ProjectID != "" {
			c.project = ch.ProjectID
		}
		cv := &changeView{ID: string(ch.ID), Title: clip(ch.Title, 200), Methodology: ch.Methodology, Namespace: ch.Namespace, Project: ch.ProjectID,
			Status: string(ch.Status), Lifecycle: ch.Lifecycle, State: ch.State}
		c.change = cv
		active, err := s.Engine.Active(authz.With(ctx, p))
		if err != nil {
			return c, fmt.Errorf("the processes cannot be read: %w", err)
		}
		for _, pr := range active {
			if pr.ChangeID == changeID && len(cv.Processes) < maxRunningShown {
				cv.Processes = append(cv.Processes, processView{ID: pr.ID, Agent: pr.Agent, Status: pr.Status})
			}
		}
		switch {
		case !startable(ch.Status):
			c.note = fmt.Sprintf("the change is %s: no agent can work on it any more", ch.Status)
			return c, nil
		case ch.Methodology == "":
			c.note = "the change has no methodology, so it names no agent"
			return c, nil
		}
		names = []string{ch.Methodology}
	} else {
		if c.project != "" {
			if ok, err := s.Projects.MayAccessProject(ctx, p, c.project); err != nil || !ok {
				return c, fmt.Errorf("you may not work on project %q", c.project)
			}
		}
		all, err := s.Projects.ApplicableMethodologies(ctx, c.project)
		if err != nil {
			return c, fmt.Errorf("the organisation cannot be read: %w", err)
		}
		names = all
	}
	applicable, err := s.Projects.ApplicableMethodologies(ctx, c.project)
	if err != nil {
		return c, fmt.Errorf("the organisation cannot be read: %w", err)
	}
	for _, n := range names {
		if !slices.Contains(applicable, n) {
			c.note = fmt.Sprintf("methodology %q does not apply to project %q", n, c.project)
			continue
		}
		m, err := s.Methodologies.Methodology(ctx, n)
		if err != nil || m == nil {
			continue
		}
		checks, err := s.Engine.CheckAgents(authz.With(ctx, p), n, c.project, nil)
		if err != nil {
			return c, fmt.Errorf("the roles cannot be checked: %w", err)
		}
		if !checks.MayStart {
			continue
		}
		for _, ag := range m.AgentList() {
			if !checks.MayRun[ag.Name] {
				continue
			}
			e := agentEntry{Methodology: n, Agent: ag.Name, Description: clip(ag.Description, 300), Examples: ag.Examples[:min(len(ag.Examples), 3)],
				Roles: ag.Roles, NeedsChange: c.change != nil}
			for _, g := range m.AgentGoals(ag) {
				e.goals = append(e.goals, g.Name)
				if len(e.Goals) < maxAgentGoals {
					e.Goals = append(e.Goals, goalEntry{Name: g.Name, Description: clip(g.Description, 200)})
				}
			}
			if c.change != nil {
				e.Running = slices.ContainsFunc(c.change.Processes, func(pv processView) bool { return pv.Agent == ag.Name })
			}
			c.agents = append(c.agents, e)
		}
	}
	if len(c.agents) > maxAgents {
		c.agents = c.agents[:maxAgents]
	}
	return c, nil
}

func (t *turn) listAgents(ctx context.Context) (any, error) {
	c, err := t.s.runnable(ctx, t.p, t.project, t.in.Context.change())
	if err != nil {
		return nil, err
	}
	res := map[string]any{"project": c.project, "agents": nonNil(c.agents)}
	if c.change != nil {
		res["change"] = c.change
	}
	if c.note != "" {
		res["note"] = c.note
	}
	if len(c.agents) == 0 && c.note == "" {
		res["note"] = "no agent of the methodologies of this project is one you may run here"
	}
	return res, nil
}

func nonNil(a []agentEntry) []agentEntry {
	if a == nil {
		return []agentEntry{}
	}
	return a
}

// startAgent validates and records a proposal; it starts nothing, creates nothing and calls no engine.
func (t *turn) startAgent(ctx context.Context, a args) (any, error) {
	for _, prev := range t.actions {
		if prev["type"] == ActionStartAgent {
			return nil, errors.New("one proposal per answer: the person decides the first before another is made")
		}
	}
	method, agent, goal, intent := a.str("methodology"), a.str("agent"), a.str("goal"), a.str("intent")
	if method == "" || agent == "" {
		return nil, errors.New(`"methodology" and "agent" are required (take them from list_agents)`)
	}
	if len(intent) > 4000 {
		return nil, errors.New("the intent is too long (4000 bytes)")
	}
	ctxChange := t.in.Context.change()
	changeID := a.str("changeId")
	nc, _ := a["newChange"].(map[string]any)
	switch {
	case changeID != "" && nc != nil:
		return nil, errors.New(`give "changeId" or "newChange", not both`)
	case ctxChange != "" && nc != nil:
		return nil, errors.New("the person is looking at a change: propose the agent on it (no newChange)")
	case ctxChange != "" && changeID == "":
		changeID = ctxChange
	case ctxChange != "" && changeID != ctxChange:
		return nil, fmt.Errorf("the only change agents are listed for is %q, the one the person is looking at", ctxChange)
	case ctxChange == "" && changeID != "":
		return nil, errors.New("no change is open: propose the agent with a newChange {title, intent}")
	case ctxChange == "" && nc == nil:
		return nil, errors.New(`no change is open: "newChange" {title, intent} is required`)
	}
	newChange := map[string]any(nil)
	if nc != nil {
		title, why := args(nc).str("title"), args(nc).str("intent")
		if title == "" || why == "" {
			return nil, errors.New(`"newChange" needs a "title" and an "intent"`)
		}
		if len(title) > 200 || len(why) > 4000 {
			return nil, errors.New("the title (200 bytes) or the intent (4000 bytes) of the new change is too long")
		}
		newChange = map[string]any{"title": title, "intent": why}
	}
	c, err := t.s.runnable(ctx, t.p, t.project, ctxChange)
	if err != nil {
		return nil, err
	}
	entry, ok := c.find(method, agent)
	if !ok {
		return nil, fmt.Errorf("agent %q of %q is not one you may run here (see list_agents)", agent, method)
	}
	if goal != "" && !slices.Contains(entry.goals, goal) {
		return nil, fmt.Errorf("agent %q has no goal %q (its goals: %s)", agent, goal, strings.Join(entry.goals, ", "))
	}
	if goal == "" && intent == "" {
		if len(entry.goals) != 1 {
			return nil, errors.New(`give a "goal" (one of the agent's) or an "intent" the engine can identify the goal from`)
		}
		goal = entry.goals[0]
	}
	label := "Start agent " + agent + " (" + method + ")"
	if newChange != nil {
		label += " on a new change"
	} else {
		label += " on " + changeID
	}
	rationale := strings.TrimSpace(a.str("rationale"))
	params := map[string]any{"methodology": method, "agent": agent, "project": c.project}
	for k, v := range map[string]string{"goal": goal, "intent": intent, "changeId": changeID} {
		if v != "" {
			params[k] = v
		}
	}
	if newChange != nil {
		params["newChange"] = newChange
	}
	act := convsvc.Action{"type": ActionStartAgent, "status": StatusProposed, "label": clip(label, 200), "args": params}
	if rationale != "" {
		act["rationale"] = clip(rationale, 500)
	}
	t.actions = append(t.actions, act)
	return map[string]any{"status": StatusProposed, "note": "proposed, awaiting the person's confirmation in the interface: nothing was started. Say what you propose and why, and do not say it started."}, nil
}

// ConfirmInput is the decision of the person on a proposal.
type ConfirmInput struct {
	ConversationID string
	MessageID      string
	ActionIndex    int
	// Decision is accept or reject; Project is the active project of the interface (the token's when empty).
	Decision string
	Project  string
}

// Confirm decides a proposal of the assistant as its conversation's owner (ADR 0090). Reject records it. Accept
// re-validates everything now, as the caller, then claims the action (a second decision is ErrDecided), creates the
// change when the proposal names a new one, starts the process through the engine as the caller and records the
// outcome in the action: started, with its process and change, or failed, with the error. A proposal that no longer
// holds is refused with ErrStale and stays proposed.
func (s *Service) Confirm(ctx context.Context, in ConfirmInput) (convsvc.Message, error) {
	p := authz.From(ctx)
	if p.Anonymous() {
		return convsvc.Message{}, convsvc.ErrAnonymous
	}
	if p.System() {
		return convsvc.Message{}, convsvc.ErrForbidden
	}
	if in.Decision != DecisionAccept && in.Decision != DecisionReject {
		return convsvc.Message{}, fmt.Errorf("%w: the decision is %s or %s", ErrInvalid, DecisionAccept, DecisionReject)
	}
	if in.Project != "" && in.Project != p.Project {
		if ok, err := s.Projects.MayAccessProject(ctx, p, in.Project); err != nil || !ok {
			return convsvc.Message{}, errors.Join(ErrForbidden, err)
		}
		p.Project = in.Project
	}
	ctx = authz.With(ctx, p)
	_, msgs, err := s.Convs.Get(ctx, p, in.ConversationID) // the owner only
	if err != nil {
		return convsvc.Message{}, err
	}
	i := slices.IndexFunc(msgs, func(m convsvc.Message) bool { return m.ID == in.MessageID })
	if i < 0 {
		return convsvc.Message{}, convsvc.ErrNotFound
	}
	msg := msgs[i]
	if msg.Role != convsvc.RoleAssistant || in.ActionIndex < 0 || in.ActionIndex >= len(msg.Actions) || (msg.Actions[in.ActionIndex]["type"] != ActionStartAgent && msg.Actions[in.ActionIndex]["type"] != ActionUITool) {
		return convsvc.Message{}, fmt.Errorf("%w: that is not a proposal", ErrInvalid)
	}
	act := msg.Actions[in.ActionIndex]
	if act["status"] != StatusProposed {
		return convsvc.Message{}, ErrDecided
	}
	sys := authz.With(context.WithoutCancel(ctx), Principal)
	// decide moves the action from the status it must be in to another: the compare-and-set that makes a decision final
	decide := func(from, to string, fn func(convsvc.Action)) (convsvc.Message, error) {
		return s.Convs.UpdateAction(sys, Principal, in.MessageID, in.ActionIndex, func(a convsvc.Action) (convsvc.Action, error) {
			if a["status"] != from {
				return nil, ErrDecided
			}
			a["status"] = to
			a["decided"] = map[string]any{"by": p.Subject, "at": s.now().UTC().Format(time.RFC3339), "decision": in.Decision}
			if fn != nil {
				fn(a)
			}
			return a, nil
		})
	}
	if in.Decision == DecisionReject {
		return decide(StatusProposed, StatusRejected, nil)
	}
	if act["type"] == ActionUITool {
		// a screen tool (ADR 0092): the server does nothing but record the decision; the web runs it through the
		// edit path of the screen and reports the outcome. Stale when the project is no longer the proposal's.
		if proj, _ := act["project"].(string); proj != p.Project {
			return convsvc.Message{}, fmt.Errorf("%w: it was made for project %q and the active project is %q", ErrStale, proj, p.Project)
		}
		return decide(StatusProposed, StatusAccepted, nil)
	}

	ar := args(mapOf(act["args"]))
	// stale: nothing is claimed, the proposal stays for the person to reject
	changeID := ar.str("changeId")
	project := ar.str("project")
	nc := args(mapOf(mapOf(act["args"])["newChange"]))
	if changeID == "" && p.Project != project {
		return convsvc.Message{}, fmt.Errorf("%w: it was made for project %q and the active project is %q", ErrStale, project, p.Project)
	}
	if changeID != "" {
		ch, err := s.readChange(ctx, p, changeID)
		if err != nil {
			return convsvc.Message{}, fmt.Errorf("%w: %v", ErrStale, err)
		}
		if !startable(ch.Status) {
			return convsvc.Message{}, fmt.Errorf("%w: change %q is %s", ErrStale, changeID, ch.Status)
		}
		project = ch.ProjectID
	}
	p.Project = project
	ctx = authz.With(ctx, p)
	c, err := s.runnable(ctx, p, project, changeID)
	if err != nil {
		return convsvc.Message{}, fmt.Errorf("%w: %v", ErrStale, err)
	}
	if _, ok := c.find(ar.str("methodology"), ar.str("agent")); !ok {
		return convsvc.Message{}, fmt.Errorf("%w: agent %q of %q is no longer one you may run here", ErrStale, ar.str("agent"), ar.str("methodology"))
	}

	if _, err := decide(StatusProposed, StatusStarting, nil); err != nil {
		return convsvc.Message{}, err
	}
	result := map[string]any{}
	fail := func(err error) (convsvc.Message, error) {
		m, uerr := decide(StatusStarting, StatusFailed, func(a convsvc.Action) {
			a["error"] = clip(err.Error(), maxErrorText)
			if len(result) > 0 {
				a["result"] = result
			}
		})
		if uerr != nil {
			s.log().Error("assistant proposal outcome not stored", "conversation", in.ConversationID, "err", uerr)
			return convsvc.Message{}, uerr
		}
		return m, nil
	}
	if len(nc) > 0 {
		m, err := s.Methodologies.Methodology(ctx, ar.str("methodology"))
		if err != nil || m == nil {
			return fail(fmt.Errorf("methodology %q cannot be read", ar.str("methodology")))
		}
		// no active project means the root project (ADR 0091)
		changeProject := project
		if changeProject == "" {
			if changeProject, err = s.Projects.RootProject(ctx); err != nil {
				return fail(err)
			}
		}
		ch, err := s.Graph.CreateChange(ctx, graph.NewChange{Title: nc.str("title"), Intent: nc.str("intent"), Methodology: m.Name, Namespace: m.Namespace,
			ProjectID: changeProject, Data: map[string]any{"createdBy": p.Subject, "via": "assistant", "conversation": in.ConversationID}})
		if err != nil {
			return fail(fmt.Errorf("the change was not created: %w", err))
		}
		changeID = string(ch.ID)
		result["changeId"] = changeID
	}
	pr, err := s.Engine.StartProcess(ctx, StartProcess{Methodology: ar.str("methodology"), Agent: ar.str("agent"), Goal: ar.str("goal"),
		Intent: ar.str("intent"), ChangeID: changeID, ProjectID: project,
		Vars: map[string]any{engine.VarConversation: in.ConversationID, "messageId": in.MessageID}})
	if err != nil {
		return fail(fmt.Errorf("the agent was not started: %w", err))
	}
	result["processId"] = pr.ID
	if pr.ChangeID != "" {
		result["changeId"] = pr.ChangeID
	}
	m, err := decide(StatusStarting, StatusStarted, func(a convsvc.Action) { a["result"] = result })
	if err != nil {
		s.log().Error("assistant proposal outcome not stored", "conversation", in.ConversationID, "process", pr.ID, "err", err)
	}
	return m, err
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// describeActions is what the history says of the actions of an assistant message: the proposals with the decision of
// the person (so the assistant can follow up), the screen tools with what became of them, then the types of the other
// actions.
func describeActions(actions []convsvc.Action) string {
	var other, notes []string
	for _, a := range actions {
		t, _ := a["type"].(string)
		if t == ActionUITool {
			notes = append(notes, describeUITool(a))
			continue
		}
		if t != ActionStartAgent {
			if t != "" {
				other = append(other, t)
			}
			continue
		}
		ar := args(mapOf(a["args"]))
		what := fmt.Sprintf("agent %s of %s", ar.str("agent"), ar.str("methodology"))
		res := mapOf(a["result"])
		switch a["status"] {
		case StatusProposed, StatusStarting:
			notes = append(notes, "proposal to start "+what+": the person has not decided yet, nothing was started")
		case StatusStarted:
			notes = append(notes, fmt.Sprintf("the person accepted the proposal to start %s: it was started (process %v on change %v)", what, res["processId"], res["changeId"]))
		case StatusRejected:
			notes = append(notes, "the person rejected the proposal to start "+what+": nothing was started")
		case StatusFailed:
			notes = append(notes, fmt.Sprintf("the person accepted the proposal to start %s but it failed: %v", what, a["error"]))
		}
	}
	var out []string
	if len(other) > 0 {
		out = append(out, "(actions run for the person: "+strings.Join(other, ", ")+")")
	}
	for _, n := range notes {
		out = append(out, "("+n+")")
	}
	return strings.Join(out, "\n")
}

// describeUITool is the history line of a screen tool action: the tool with its arguments, and the decision and
// outcome known so far.
func describeUITool(a convsvc.Action) string {
	what := fmt.Sprintf("%s%v", PrefixUI, a["tool"])
	if s := shortArgs(mapOf(a["args"])); s != "" {
		what += " " + s
	}
	switch a["status"] {
	case StatusRequested:
		return "the interface was asked to run " + what + ": no outcome is known"
	case StatusProposed:
		return "proposal " + what + ": the person has not decided yet, nothing was changed"
	case StatusAccepted:
		return "the person accepted the proposal " + what + ": the interface is applying it, no outcome reported yet"
	case StatusRejected:
		return "the person rejected the proposal " + what + ": nothing was changed"
	case StatusDone:
		return what + " was done in the interface"
	case StatusFailed:
		return fmt.Sprintf("%s failed in the interface: %v", what, a["error"])
	}
	return what
}
