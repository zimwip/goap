package builtin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/brief"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
)

// Change is the goap-change connector: it works on a change, the blackboard every modification of
// the graph goes through (CLAUDE.md, rule 2). A node is declared on the change (a change impact,
// ADR 0024) the first time a tool touches it, then written on the change branch; nothing lands on
// main until the change is applied, which stays a decision of the methodology or of a reviewer.
// Without a change argument the tools work on the change of the calling process.
type Change struct{ p Ports }

var _ connectorkit.Connector = Change{}

var changeOps = []op{
	{"create", "Open a change on the head of main of a namespace: {change}", schema(map[string]string{"title": "string", "intent": "string", "namespace": "string", "methodology": "string", "unit": "string"}, "title", "intent", "namespace")},
	{"read", "Read a change: {change, nodes, items}", schema(map[string]string{"change": "string"})},
	{"list", "List changes, the latest first: {changes, truncated}", schema(map[string]string{"namespace": "string", "unit": "string", "status": "string", "limit": "integer"})},
	{"reformulate", "Revise the title/intent of a change, superseding the previous definition (history kept): {change, item}",
		schema(map[string]string{"change": "string", "title": "string", "intent": "string", "rationale": "string"}, "intent", "rationale")},
	{"write", "Create a node in the change: {node}", schema(map[string]string{"change": "string", "key": "string", "type": "string", "properties": "object", "rationale": "string"}, "key", "type", "rationale")},
	{"edit", "Modify the properties or the state of a node in the change: {node}", schema(map[string]string{"change": "string", "key": "string", "properties": "object", "expect": "object", "state": "string", "rationale": "string"}, "key", "rationale")},
	{"link", "Add a link from a node of the change: {node}", schema(map[string]string{"change": "string", "from": "string", "type": "string", "to": "string", "rationale": "string"}, "from", "type", "to")},
	{"retire", "Retire a node in the change: {node}", schema(map[string]string{"change": "string", "key": "string", "rationale": "string"}, "key", "rationale")},
	{"note", "Add an artifact item to the blackboard: {item}", schema(map[string]string{"change": "string", "type": "string", "text": "string", "data": "object"}, "text")},
	{"signal", "Emit a named notification other agents or a live parent may react to: {item}", schema(map[string]string{"change": "string", "type": "string", "data": "object", "target": "string"}, "type")},
	{"validate", "Check the consistency of the change: {issues}", schema(map[string]string{"change": "string"})},
	{"options", "List the options of the change and the active one: {active, options}", schema(map[string]string{"change": "string"})},
	{"option", "Open an option of the change, a hypothesis explored on its own flow: {option}", schema(map[string]string{"change": "string", "name": "string", "hypothesis": "string", "activate": "boolean"}, "name", "hypothesis")},
	{"activate", "Work on an option of the change (\"main\": the main flow): {active}", schema(map[string]string{"change": "string", "option": "string"}, "option")},
	{"evaluate", "Record the evaluation of an option: {option}", schema(map[string]string{"change": "string", "option": "string", "comment": "string"}, "option", "comment")},
	{"compare", "Compare the options of the change on the nodes they changed: {level, options, nodes}", schema(map[string]string{"change": "string", "level": "string", "all": "boolean"})},
	{"decisions", "List the decision points of the change and their questions: {points}", schema(map[string]string{"change": "string"})},
	{"decision", "Open a decision point on a question, among the open options by default: {point}", schema(map[string]string{"change": "string", "question": "string", "options": "array", "criteria": "array", "decider": "string", "threshold": "number", "maxRounds": "integer", "maxDuration": "string"}, "question")},
	{"rule", "Rule a decision point: decided (option, confidence, justification) or undecidable (justification, questions): {point}", schema(map[string]string{"change": "string", "point": "string", "outcome": "string", "option": "string", "confidence": "number", "justification": "string", "questions": "array"}, "outcome", "justification")},
	{"answer", "Answer an open question of a decision point: {point}", schema(map[string]string{"change": "string", "question": "string", "answer": "string"}, "question", "answer")},
	{"brief", "The change in brief, one line per fact (intent, impacts, decisions, risks, actions, latest artifacts): {brief}", schema(map[string]string{"change": "string"})},
	{"trace", "Follow an information through the change — an item id, a risk or action key, or a node key: what produced it, what it derives from, what it led to, what replaced it: {trace}",
		schema(map[string]string{"change": "string", "ref": "string"}, "ref")},
	{"risks", "The risk register and the actions of the change: {risks, actions}", schema(map[string]string{"change": "string"})},
	{"risk", "Raise a risk, or update one by its key (a new version keeps what it does not restate); probability and impact 1-5, status open|mitigating|accepted|occurred|closed: {risk}",
		schema(map[string]string{"change": "string", "key": "string", "title": "string", "description": "string", "probability": "integer", "impact": "integer",
			"status": "string", "owner": "string", "actions": "array", "rationale": "string"})},
	{"action", "Create an action, or update one by its key; status open|done|cancelled, for: the risk key or decision point it answers: {action}",
		schema(map[string]string{"change": "string", "key": "string", "title": "string", "status": "string", "owner": "string", "due": "string", "for": "string",
			"result": "string"})},
}

// Info implements connectorkit.Connector.
func (Change) Info() *connectorv1.ConnectorInfo {
	return info(mcp.BuiltinChange, "Changes of the platform, the blackboard of every modification, worked on for the caller (built in).", changeOps)
}

// changeID is the change a call names, else the change of the calling process.
func changeID(ctx context.Context, a args) (domain.ChangeID, error) {
	if id := a.str("change"); id != "" {
		return domain.ChangeID(id), nil
	}
	if id := mcp.CallFrom(ctx).Change; id != "" {
		return domain.ChangeID(id), nil
	}
	return "", errors.New(`argument "change" is required outside a process`)
}

// Invoke implements connectorkit.Connector.
func (c Change) Invoke(ctx context.Context, op string, raw, _ map[string]any, _ map[string]string) (map[string]any, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	a := args(raw)
	switch op {
	case "create":
		return c.create(ctx, who, a)
	case "list":
		return c.list(ctx, a)
	}
	id, err := changeID(ctx, a)
	if err != nil {
		return nil, err
	}
	switch op {
	case "options", "option", "activate", "evaluate", "compare":
		return c.options(ctx, who, id, op, a)
	case "decisions", "decision", "rule", "answer":
		return c.decisions(ctx, who, id, op, a)
	}
	bb, err := c.p.Graph.Blackboard(ctx, id)
	if err != nil {
		return nil, err
	}
	w := &working{c: c, ctx: ctx, who: who, bb: bb}
	switch op {
	case "read":
		return result(map[string]any{"change": changeSummary(bb.Change), "nodes": w.impacts(), "items": bb.Change.Items})
	case "reformulate":
		return c.reformulate(ctx, bb, a)
	case "validate":
		issues, err := c.p.Graph.ValidateBoard(ctx, id, "")
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"issues": issues})
	case "brief":
		return result(map[string]any{"brief": brief.Of(bb, nil)})
	case "trace":
		ref, err := a.required("ref")
		if err != nil {
			return nil, err
		}
		t, err := brief.Trace(bb.Change, ref)
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"trace": t})
	case "risks":
		return result(map[string]any{"risks": bb.Change.Risks(), "actions": bb.Change.ActionItems()})
	case "risk", "action":
		return c.record(ctx, bb, op, a)
	case "note":
		text, err := a.required("text")
		if err != nil {
			return nil, err
		}
		typ := a.str("type")
		if typ == "" {
			typ = "note"
		}
		data := map[string]any{}
		for k, v := range a.object("data") {
			data[k] = v
		}
		data["text"] = text
		items, err := c.p.Graph.AddItems(ctx, id, []domain.ChangeItem{{Kind: domain.KindArtifact, Type: typ, Status: domain.ItemProposed,
			Data: data, ProducedBy: producer(ctx)}})
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"item": items[0]})
	case "signal":
		typ, err := a.required("type")
		if err != nil {
			return nil, err
		}
		items, err := c.p.Graph.AddItems(ctx, id, []domain.ChangeItem{{Kind: domain.KindSignal, Type: typ, Status: domain.ItemProposed,
			Data: a.object("data"), Target: a.str("target"), ProducedBy: producer(ctx)}})
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"item": items[0]})
	case "write":
		key, typ := a.str("key"), a.str("type")
		if key == "" || typ == "" {
			return nil, errors.New(`arguments "key" and "type" are required`)
		}
		if err := c.gate(ctx, who, typ); err != nil {
			return nil, err
		}
		if _, ok := w.impact(key); ok {
			return nil, fmt.Errorf("%s is already in the change: edit it", key)
		}
		if _, ok, err := w.base(key); err != nil {
			return nil, err
		} else if ok {
			return nil, fmt.Errorf("%s exists in the baseline of the change: edit it", key)
		}
		imp, err := w.declare(domain.ChangeImpact{Key: key, Type: typ, Intent: domain.IntentCreated, Rationale: a.str("rationale")})
		if err != nil {
			return nil, err
		}
		return w.write(imp, graph.NodeWrite{Properties: a.object("properties")})
	case "edit":
		imp, err := w.touch(a.str("key"), a.str("rationale"))
		if err != nil {
			return nil, err
		}
		if err := w.expect(imp, a.object("expect")); err != nil {
			return nil, err
		}
		return w.write(imp, graph.NodeWrite{Properties: a.object("properties"), State: a.str("state")})
	case "retire":
		imp, err := w.touch(a.str("key"), a.str("rationale"))
		if err != nil {
			return nil, err
		}
		if imp.Intent == domain.IntentCreated {
			return nil, fmt.Errorf("%s is created by the change: it cannot be retired, drop it from the change", imp.Key)
		}
		return w.write(imp, graph.NodeWrite{Retire: true})
	case "link":
		typ, to := a.str("type"), a.str("to")
		if typ == "" || to == "" {
			return nil, errors.New(`arguments "type" and "to" are required`)
		}
		rationale := a.str("rationale")
		if rationale == "" {
			rationale = "link " + typ + " to " + to
		}
		imp, err := w.touch(a.str("from"), rationale)
		if err != nil {
			return nil, err
		}
		ref, err := w.ref(to)
		if err != nil {
			return nil, err
		}
		return w.write(imp, graph.NodeWrite{AddLinks: []graph.LinkWrite{{Type: typ, To: ref}}})
	}
	return nil, unknown(op)
}

// options works on the options of a change (ADR 0009 §3, ADR 0032 §6); selecting or rejecting one is a decision
// made through the graph service, not a tool.
func (c Change) options(ctx context.Context, who authz.Principal, id domain.ChangeID, op string, a args) (map[string]any, error) {
	summary := func(f domain.Flow) map[string]any {
		out := map[string]any{"id": f.ID, "status": f.OptionStatus(), "active": f.Active}
		if f.Option != nil {
			out["name"], out["hypothesis"] = f.Option.Name, f.Option.Hypothesis
		}
		if f.Evaluation != "" {
			out["evaluation"] = f.Evaluation
		}
		return out
	}
	switch op {
	case "options":
		os, err := c.p.Graph.Options(ctx, id)
		if err != nil {
			return nil, err
		}
		list, active := []map[string]any{}, domain.MainFlow
		for _, f := range os {
			list = append(list, summary(f))
			if f.Active {
				active = f.ID
			}
		}
		return result(map[string]any{"active": active, "options": list})
	case "option":
		name, err := a.required("name")
		if err != nil {
			return nil, err
		}
		f, err := c.p.Graph.OpenOption(ctx, id, graph.OpenOptionRequest{Name: name, Hypothesis: a.str("hypothesis"), Activate: a.boolean("activate"), By: who.Subject})
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"option": summary(f)})
	case "activate":
		active, err := c.p.Graph.ActivateOption(ctx, id, a.str("option"), who.Subject)
		if err != nil {
			return nil, err
		}
		if active == "" {
			active = domain.MainFlow
		}
		return result(map[string]any{"active": active})
	case "evaluate":
		option, err := a.required("option")
		if err != nil {
			return nil, err
		}
		f, err := c.p.Graph.EvaluateOption(ctx, id, option, who.Subject, a.str("comment"))
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"option": summary(f)})
	}
	cmp, err := c.p.Graph.CompareOptions(ctx, id, a.str("level"), a.boolean("all"))
	if err != nil {
		return nil, err
	}
	opts := []map[string]any{}
	for _, f := range cmp.Options {
		opts = append(opts, summary(f))
	}
	return result(map[string]any{"level": cmp.Level, "options": opts, "nodes": cmp.Nodes})
}

// decisions works on the decision points of a change (ADR 0009 §4). A ruling made through a tool is an agent's:
// below the threshold of its point it waits for a person's ratification, which is not a tool.
func (c Change) decisions(ctx context.Context, who authz.Principal, id domain.ChangeID, op string, a args) (map[string]any, error) {
	strs := func(name string) []string {
		var out []string
		if l, ok := a[name].([]any); ok {
			for _, v := range l {
				out = append(out, fmt.Sprint(v))
			}
		}
		return out
	}
	num := func(name string) float64 {
		f, _ := a[name].(float64)
		return f
	}
	var d domain.DecisionPoint
	var err error
	switch op {
	case "decisions":
		ps, err := c.p.Graph.DecisionPoints(ctx, id)
		if err != nil {
			return nil, err
		}
		if ps == nil {
			ps = []domain.DecisionPoint{}
		}
		return result(map[string]any{"points": ps})
	case "decision":
		in := graph.OpenDecisionRequest{Question: a.str("question"), Criteria: strs("criteria"), Decider: a.str("decider"), Threshold: num("threshold"),
			MaxRounds: int(num("maxRounds")), By: who.Subject}
		if _, ok := a["options"]; ok {
			in.Options = append([]string{}, strs("options")...)
		}
		if s := a.str("maxDuration"); s != "" {
			if in.MaxDuration, err = time.ParseDuration(s); err != nil {
				return nil, fmt.Errorf("maxDuration: %w", err)
			}
		}
		d, err = c.p.Graph.OpenDecision(ctx, id, in)
	case "rule":
		d, err = c.p.Graph.RuleDecision(ctx, id, graph.RuleRequest{Point: a.str("point"), Outcome: a.str("outcome"), Option: a.str("option"),
			Confidence: num("confidence"), Justification: a.str("justification"), Questions: strs("questions"), By: who.Subject})
	case "answer":
		d, err = c.p.Graph.AnswerQuestion(ctx, id, a.str("question"), a.str("answer"), mcp.CallFrom(ctx).Process, who.Subject)
	}
	if err != nil {
		return nil, err
	}
	return result(map[string]any{"point": d})
}

// create opens a change: intent (why), unit (who), methodology (how) and namespace (what).
func (c Change) create(ctx context.Context, who authz.Principal, a args) (map[string]any, error) {
	title, intent, ns := a.str("title"), a.str("intent"), a.str("namespace")
	if title == "" || intent == "" || ns == "" {
		return nil, errors.New(`arguments "title", "intent" and "namespace" are required`)
	}
	head, err := c.p.Graph.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		return nil, fmt.Errorf("head of main of %s: %w", ns, err)
	}
	ch, err := c.p.Graph.CreateChange(ctx, graph.NewChange{Title: title, Intent: intent, Methodology: a.str("methodology"), Namespace: ns,
		BaselineID: head.ID, OwnerOrg: a.unit(ctx, who), Data: map[string]any{"createdBy": who.Subject, "via": mcp.BuiltinChange}})
	if err != nil {
		return nil, err
	}
	return result(map[string]any{"change": changeSummary(ch)})
}

// intentItemType is the item type reformulate uses to keep the history of a change's definition:
// each revision supersedes the previous one (never overwritten), the header (title, intent) always
// holding the current one.
const intentItemType = "intent"

// latestIntentItem returns the current "intent" item of a change, if any has been recorded yet
// (none: the change is still on the definition it was created with).
func latestIntentItem(c domain.Change) *domain.ChangeItem {
	for i := len(c.Items) - 1; i >= 0; i-- {
		if c.Items[i].Kind == domain.KindArtifact && c.Items[i].Type == intentItemType {
			return &c.Items[i]
		}
	}
	return nil
}

// reformulate revises the definition (title, intent) of a change: the current one is superseded as
// an "intent" item, never erased, so the change keeps its full history; the header is then patched
// to the new definition, the one prompts and reads see.
func (c Change) reformulate(ctx context.Context, bb domain.Blackboard, a args) (map[string]any, error) {
	newIntent, err := a.required("intent")
	if err != nil {
		return nil, err
	}
	rationale, err := a.required("rationale")
	if err != nil {
		return nil, err
	}
	title := a.str("title")
	if title == "" {
		title = bb.Change.Title
	}
	id := bb.Change.ID
	prev := latestIntentItem(bb.Change)
	if prev == nil {
		// the definition set at creation has no item of its own yet: snapshot it first, so the
		// history stays complete in the items once the header is overwritten below.
		snap, err := c.p.Graph.AddItems(ctx, id, []domain.ChangeItem{{Kind: domain.KindArtifact, Type: intentItemType, Status: domain.ItemAccepted,
			Data: map[string]any{"title": bb.Change.Title, "intent": bb.Change.Intent}, ProducedBy: "create"}})
		if err != nil {
			return nil, err
		}
		prev = &snap[0]
	}
	items, err := c.p.Graph.AddItems(ctx, id, []domain.ChangeItem{{Kind: domain.KindArtifact, Type: intentItemType, Status: domain.ItemAccepted,
		Data: map[string]any{"title": title, "intent": newIntent, "rationale": rationale}, ProducedBy: producer(ctx), Supersedes: []domain.ItemID{prev.ID}}})
	if err != nil {
		return nil, err
	}
	ch, err := c.p.Graph.UpdateChange(ctx, id, graph.ChangePatch{Title: &title, Intent: &newIntent})
	if err != nil {
		return nil, err
	}
	return result(map[string]any{"change": changeSummary(ch), "item": items[0]})
}

// list lists changes matching a filter, the latest first: the caller checks whether a request
// continues one of them before opening a new one (goap-change.create).
func (c Change) list(ctx context.Context, a args) (map[string]any, error) {
	f := graph.ChangesFilter{Namespace: a.str("namespace"), OwnerOrg: a.str("unit")}
	if st := a.str("status"); st != "" {
		f.Status = []domain.ChangeStatus{domain.ChangeStatus(st)}
	}
	cs, err := c.p.Graph.ListChanges(ctx, f)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(cs, func(x, y domain.Change) int { return y.CreatedAt.Compare(x.CreatedAt) })
	truncated := len(cs) > a.limit()
	if truncated {
		cs = cs[:a.limit()]
	}
	list := make([]map[string]any, 0, len(cs))
	for _, ch := range cs {
		list = append(list, changeSummary(ch))
	}
	return result(map[string]any{"changes": list, "truncated": truncated})
}

// gate applies the access gate of the platform to the access nodes (User, Policy: ADR 0020).
func (c Change) gate(ctx context.Context, who authz.Principal, typ string) error {
	if typ != access.NodeTypeUser && typ != access.NodeTypePolicy {
		return nil
	}
	gate := c.p.Floor
	if gate == nil {
		gate = c.p.Authz
	}
	if gate == nil {
		return fmt.Errorf("%s nodes cannot be changed without an authorizer: %w", typ, authz.ErrForbidden)
	}
	return authz.Check(ctx, gate, authz.Request{Subject: who, Action: "write", Resource: authz.Resource{Type: access.ResourcePolicy, Org: who.Org}})
}

// producer names what produced an item or a write: the calling process when there is one.
func producer(ctx context.Context) string {
	if p := mcp.CallFrom(ctx).Process; p != "" {
		return p
	}
	return mcp.BuiltinChange
}

func changeSummary(c domain.Change) map[string]any {
	return map[string]any{"id": c.ID, "title": c.Title, "intent": c.Intent, "methodology": c.Methodology, "namespace": c.Namespace,
		"unit": domain.OrgOf(c.OwnerOrg), "status": c.Status, "baselineId": c.BaselineID, "branch": c.Branch, "parentId": c.ParentID}
}

// working is one call on the blackboard of a change.
type working struct {
	c   Change
	ctx context.Context
	who authz.Principal
	bb  domain.Blackboard

	baseNodes []domain.Node
	baseRead  bool
}

// impact returns the change impact of a node as the flow the call works on sees it (the main flow, or the active
// option: the blackboard is the one of that flow).
func (w *working) impact(key string) (domain.ChangeImpact, bool) {
	for _, n := range w.bb.Change.Nodes {
		if n.Key == key && !n.Superseded {
			return n, true
		}
	}
	return domain.ChangeImpact{}, false
}

func (w *working) impacts() []map[string]any {
	out := []map[string]any{}
	for _, n := range w.bb.Change.Nodes {
		if n.Superseded {
			continue
		}
		v := map[string]any{"key": n.Key, "type": n.Type, "intent": n.Intent, "rationale": n.Rationale, "review": n.Review, "planned": n.Planned()}
		if n.Flow != "" {
			v["flow"] = n.Flow
		}
		if n.Post != nil {
			if nv, ok := w.bb.Nodes[*n.Post]; ok {
				v["properties"], v["state"], v["retired"] = nv.Properties, nv.State, nv.Deleted
			}
		}
		out = append(out, v)
	}
	return out
}

// base returns a node of the reference baseline of the change.
func (w *working) base(key string) (domain.Node, bool, error) {
	if !w.baseRead {
		nodes, _, err := w.c.p.Graph.BaselineGraph(w.ctx, w.bb.Change.BaselineID)
		if err != nil {
			return domain.Node{}, false, err
		}
		w.baseNodes, w.baseRead = nodes, true
	}
	for _, n := range w.baseNodes {
		if n.Key == key && (w.bb.Change.Namespace == "" || n.Namespace == w.bb.Change.Namespace) && !n.Deleted {
			return n, true, nil
		}
	}
	return domain.Node{}, false, nil
}

func (w *working) declare(imp domain.ChangeImpact) (domain.ChangeImpact, error) {
	if imp.Rationale == "" {
		return imp, fmt.Errorf("%s: a rationale is required", imp.Key)
	}
	imp.Review, imp.ProducedBy = domain.ReviewProposed, producer(w.ctx)
	out, err := w.c.p.Graph.AddNodes(w.ctx, w.bb.Change.ID, []domain.ChangeImpact{imp})
	if err != nil {
		return imp, err
	}
	if len(out) == 0 {
		return imp, fmt.Errorf("%s: the change did not declare the node", imp.Key)
	}
	w.bb.Change.Nodes = append(w.bb.Change.Nodes, out[0])
	return out[0], nil
}

// touch returns the change impact of a node, declaring the modification of a node of the baseline
// the first time the change touches it.
func (w *working) touch(key, rationale string) (domain.ChangeImpact, error) {
	if key == "" {
		return domain.ChangeImpact{}, errors.New("the key of the node is required")
	}
	if imp, ok := w.impact(key); ok {
		return imp, w.c.gate(w.ctx, w.who, imp.Type)
	}
	n, ok, err := w.base(key)
	if err != nil {
		return domain.ChangeImpact{}, err
	}
	if !ok {
		return domain.ChangeImpact{}, fmt.Errorf("no node %s in the change nor in its baseline", key)
	}
	if err := w.c.gate(w.ctx, w.who, n.Type); err != nil {
		return domain.ChangeImpact{}, err
	}
	pre := n.Ref()
	return w.declare(domain.ChangeImpact{Key: key, Type: n.Type, Intent: domain.IntentModified, Rationale: rationale, Pre: &pre})
}

// current returns the properties of the node as the change sees it.
func (w *working) current(imp domain.ChangeImpact) map[string]any {
	for _, r := range []*domain.NodeRef{imp.Post, imp.Pre} {
		if r == nil {
			continue
		}
		if nv, ok := w.bb.Nodes[*r]; ok {
			return nv.Properties
		}
		if r == imp.Pre {
			if n, ok, _ := w.base(imp.Key); ok {
				return n.Properties
			}
		}
	}
	return nil
}

// expect checks the values an edit expects, so that an edit does not overwrite a concurrent one.
func (w *working) expect(imp domain.ChangeImpact, want map[string]any) error {
	cur := w.current(imp)
	for k, v := range want {
		if got := cur[k]; !reflect.DeepEqual(normalize(got), normalize(v)) {
			return fmt.Errorf("%s: %s is %v, the edit expects %v", imp.Key, k, got, v)
		}
	}
	return nil
}

// normalize makes values read from JSON and from the graph comparable.
func normalize(v any) any {
	m, err := result(map[string]any{"v": v})
	if err != nil {
		return v
	}
	return m["v"]
}

// ref returns the version a link points to: the one the change writes, else the one of the baseline.
func (w *working) ref(key string) (domain.NodeRef, error) {
	if imp, ok := w.impact(key); ok && imp.Post != nil {
		return *imp.Post, nil
	}
	if n, ok, err := w.base(key); err != nil {
		return domain.NodeRef{}, err
	} else if ok {
		return n.Ref(), nil
	}
	return domain.NodeRef{}, fmt.Errorf("no node %s to link to in the change nor in its baseline", key)
}

func (w *working) write(imp domain.ChangeImpact, nw graph.NodeWrite) (map[string]any, error) {
	out, err := w.c.p.Graph.WriteNode(w.ctx, w.bb.Change.ID, imp.ID, nw)
	if err != nil {
		return nil, err
	}
	return result(map[string]any{"node": map[string]any{"key": out.Key, "type": out.Type, "intent": out.Intent, "review": out.Review, "post": out.Post}})
}

// record raises or updates a risk or an action of the change (ADR 0036 §1): a new version of the record of its key,
// keeping what it does not restate. Without a key, a new record gets the next free one (RSK-n, ACT-n).
func (c Change) record(ctx context.Context, bb domain.Blackboard, op string, a args) (map[string]any, error) {
	kind, prefix, fields := domain.KindRisk, "RSK-", []string{"title", "description", "status", "owner"}
	if op == "action" {
		kind, prefix, fields = domain.KindAction, "ACT-", []string{"title", "status", "owner", "due", "for", "result"}
	}
	key := a.str("key")
	var existing bool
	n := 0
	for _, it := range bb.Change.Items {
		if it.Kind != kind {
			continue
		}
		k, _ := it.Data["key"].(string)
		existing = existing || (key != "" && k == key)
		var i int
		if _, err := fmt.Sscanf(k, prefix+"%d", &i); err == nil && i > n {
			n = i
		}
	}
	if key == "" {
		key = fmt.Sprintf("%s%d", prefix, n+1)
	}
	data := map[string]any{"key": key}
	for _, f := range fields {
		if v := a.str(f); v != "" {
			data[f] = v
		}
	}
	if kind == domain.KindRisk {
		for _, f := range []string{"probability", "impact"} {
			if v, ok := a[f].(float64); ok {
				data[f] = v
			}
		}
		if acts, ok := a["actions"].([]any); ok {
			data["actions"] = acts
		}
		if r := a.str("rationale"); r != "" {
			data["rationale"] = r
		}
	}
	if !existing && a.str("title") == "" {
		return nil, fmt.Errorf("a new %s needs a title", op)
	}
	if existing {
		// the title is required on every version: keep the current one
		for _, it := range slices.Backward(bb.Change.Items) {
			if k, _ := it.Data["key"].(string); it.Kind == kind && k == key {
				if _, ok := data["title"]; !ok {
					data["title"] = it.Data["title"]
				}
				break
			}
		}
	}
	items, err := c.p.Graph.AddItems(ctx, bb.Change.ID, []domain.ChangeItem{{Kind: kind, Type: op, Status: domain.ItemProposed, Data: data, ProducedBy: producer(ctx)}})
	if err != nil {
		return nil, err
	}
	return result(map[string]any{op: items[0]})
}
