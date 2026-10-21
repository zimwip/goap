package builtin

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/criticality"
	"github.com/zimwip/goap/contracts/decision"
	"github.com/zimwip/goap/contracts/domain"
	connectorv1 "github.com/zimwip/goap/contracts/gen/goap/connector/v1"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/contracts/mcp"
	"github.com/zimwip/goap/contracts/risk"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/brief"
	"github.com/zimwip/goap/lib/mcpbuiltin"
	"github.com/zimwip/goap/mcp/connectorkit"
)

// Change is the goap-change connector: it works on a change, the blackboard every modification of
// the graph goes through (CLAUDE.md, rule 2). A node is declared on the change (a change impact,
// ADR 0024) the first time a tool touches it, then written on the change branch; nothing commits on
// main until the change is applied, which stays a decision of the methodology or of a reviewer.
// Without a change argument the tools work on the change of the calling process.
type Change struct{ p Ports }

var _ connectorkit.Connector = Change{}

var changeOps = []op{
	{"create", "Open a change on the head of main of a namespace, or a sub-change of a parent (on its branch, its namespace and project): {change}", schema(map[string]string{"title": "string", "intent": "string", "namespace": "string", "methodology": "string", "unit": "string", "project": "string", "parent": "string", "criticality": "string", "data": "object"}, "title", "intent")},
	{"close", "Abandon an open change with the reason (the calling change or one of its sub-changes, from a process): {change}", schema(map[string]string{"change": "string", "reason": "string"}, "change", "reason")},
	{"move", "Move a root change, draft or active, with its sub-changes, to another project: {change}", schema(map[string]string{"change": "string", "project": "string"}, "project")},
	{"attach", "Attach a root change to a change whose methodology integrates its own, as its sub-change: {change}", schema(map[string]string{"change": "string", "parent": "string"}, "parent")},
	{"detach", "Detach a sub-change from the change integrating it, a root change again: {change}", schema(map[string]string{"change": "string"})},
	{"depend", "Add or remove changes this change depends on: {change, add: [ids], remove: [ids]}", schema(map[string]string{"change": "string", "add": "array", "remove": "array"})},
	{"upgrade", "Move an open change to another version of its methodology (default: the latest): {change}", schema(map[string]string{"change": "string", "version": "string"})},
	{"tag", "Name one exact version of a node (default: the version at the head of main): {tag}", schema(map[string]string{"namespace": "string", "key": "string", "version": "integer", "name": "string"}, "namespace", "key", "name")},
	{"dependencies", "Read the dependencies of a change, its dependents and the merge order of its sub-changes: {change}", schema(map[string]string{"change": "string"})},
	{"read", "Read a change: {change, nodes, items}", schema(map[string]string{"change": "string"})},
	{"list", "List changes, the latest first: {changes, truncated}", schema(map[string]string{"namespace": "string", "unit": "string", "status": "string", "limit": "integer"})},
	{"reformulate", "Revise the title/intent of a change, superseding the previous definition (history kept): {change, item}",
		schema(map[string]string{"change": "string", "title": "string", "intent": "string", "rationale": "string"}, "intent", "rationale")},
	{"write", "Create a node in the change: {node}", schema(map[string]string{"change": "string", "key": "string", "type": "string", "properties": "object", "rationale": "string"}, "key", "type", "rationale")},
	{"edit", "Modify the properties of a node in the change (its draft, the node being checked out on the first edit), or move it to a lifecycle state (a transition of the draft, the node checked out first when it has none): {node}", schema(map[string]string{"change": "string", "key": "string", "properties": "object", "expect": "object", "state": "string", "rationale": "string"}, "key", "rationale")},
	{"link", "Add a link from a node of the change: {node}", schema(map[string]string{"change": "string", "from": "string", "type": "string", "to": "string", "rationale": "string"}, "from", "type", "to")},
	{"unlink", "Remove a link from a node of the change (removing a child is a modification of its parent): {node}", schema(map[string]string{"change": "string", "from": "string", "type": "string", "to": "string", "rationale": "string"}, "from", "type", "to")},
	{"merge", "Merge nodes into a new one, seen from their parents (the sources stay as they are): {successors, sources, parents, suspect}", schema(map[string]string{"change": "string", "sources": "array", "key": "string", "type": "string", "properties": "object", "rationale": "string"}, "sources", "key", "type", "rationale")},
	{"split", "Split a node into new ones, seen from its parents (the node stays as it is): {successors, sources, parents, suspect}", schema(map[string]string{"change": "string", "source": "string", "into": "array"}, "source", "into")},
	{"cancel", "Cancel the checkout of a node of the change: its draft is dropped (a node created by the change goes away): {node}", schema(map[string]string{"change": "string", "key": "string"}, "key")},
	{"remove", "Take a node out of the change (its draft is dropped), or several at once with keys (all or none): {removed}", schema(map[string]string{"change": "string", "key": "string", "keys": "array"})},
	{"note", "Add an artifact item to the blackboard (target: what the artifact is, default note): {item}", schema(map[string]string{"change": "string", "target": "string", "text": "string", "data": "object"}, "text")},
	{"signal", "Emit a named notification other agents or a live parent may react to (target: the signal's name, recipient: a process id, empty for all): {item}", schema(map[string]string{"change": "string", "target": "string", "data": "object", "recipient": "string"}, "target")},
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
	{"derogations", "The derogations of the change (rule, target, reason, signatory, expires, status): {derogations}", schema(map[string]string{"change": "string"})},
	{"derogation", "Sign a derogation, or update / close one by its key (needs derogation:sign; the signatory is the caller); expires is a date or RFC 3339, status open|closed: {derogation}",
		schema(map[string]string{"change": "string", "key": "string", "rule": "string", "target": "string", "reason": "string", "expires": "string", "status": "string"})},
	{"reviews", "The reviews of the change (comment, status open|submitted|discarded, entries: impact, comment, outcome accept|reject) and the impacts that await review: {reviews, awaiting}", schema(map[string]string{"change": "string", "flow": "string"})},
	{"review_open", "Open a review: a global comment and, optionally, the impacts (ids or node keys) it takes: {review}", schema(map[string]string{"change": "string", "flow": "string", "comment": "string", "impacts": "array"})},
	{"review_update", "Change an open review (its author or an administrator): the comment, impacts to add / remove, entries [{impact, comment, outcome accept|reject}]: {review}", schema(map[string]string{"change": "string", "review": "string", "comment": "string", "add": "array", "remove": "array", "entries": "array"}, "review")},
	{"review_submit", "Submit an open review: every entry is reviewed with its comment and the global one, all or none; a refusal names the entry and leaves the review open: {review}", schema(map[string]string{"change": "string", "review": "string"}, "review")},
	{"review_discard", "Discard an open review that was never submitted: {review}", schema(map[string]string{"change": "string", "review": "string"}, "review")},
	{"action", "Create an action, or update one by its key; status open|done|cancelled, for: the risk key or decision point it answers: {action}",
		schema(map[string]string{"change": "string", "key": "string", "title": "string", "status": "string", "owner": "string", "due": "string", "for": "string",
			"result": "string", "kind": "string", "purpose": "string", "risks": "array", "hypotheses": "array", "subChange": "string"})},
}

// Info implements connectorkit.Connector.
func (Change) Info() *connectorv1.ConnectorInfo {
	return info(mcpbuiltin.Change, "Changes of the platform, the blackboard of every modification, worked on for the caller (built in).", changeOps)
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
	case "tag":
		return c.tag(ctx, a)
	}
	id, err := changeID(ctx, a)
	if err != nil {
		return nil, err
	}
	switch op {
	case "close":
		return c.close(ctx, id, a)
	case "move":
		moved, err := c.p.Graph.MoveChange(ctx, id, a.str("project"))
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"change": changeSummary(moved)})
	case "attach":
		attached, err := c.p.Graph.AttachChange(ctx, id, domain.ChangeID(a.str("parent")))
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"change": changeSummary(attached)})
	case "detach":
		detached, err := c.p.Graph.DetachChange(ctx, id)
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"change": changeSummary(detached)})
	case "depend":
		cur, err := c.p.Graph.Change(ctx, id)
		if err != nil {
			return nil, err
		}
		deps := slices.Clone(cur.DependsOn)
		for _, d := range asStrings(a["add"]) {
			if d != "" && !slices.Contains(deps, domain.ChangeID(d)) {
				deps = append(deps, domain.ChangeID(d))
			}
		}
		for _, d := range asStrings(a["remove"]) {
			deps = slices.DeleteFunc(deps, func(x domain.ChangeID) bool { return x == domain.ChangeID(d) })
		}
		set, err := c.p.Graph.SetChangeDependencies(ctx, id, deps)
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"change": changeSummary(set)})
	case "upgrade":
		up, err := c.p.Graph.UpgradeChange(ctx, id, a.str("version"))
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"change": changeSummary(up)})
	case "dependencies":
		d, err := c.p.Graph.ChangeDependencies(ctx, id)
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"dependsOn": d.DependsOn, "dependents": d.Dependents, "mergeOrder": d.Order})
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
		return result(map[string]any{"risks": risk.Risks(bb.Change), "actions": risk.Actions(bb.Change)})
	case "risk", "action":
		return c.record(ctx, bb, op, a)
	case "reviews":
		return c.reviews(ctx, id, a)
	case "review_open", "review_update", "review_submit", "review_discard":
		return c.review(ctx, bb, op, a)
	case "derogations":
		return result(map[string]any{"derogations": risk.Derogations(bb.Change)})
	case "derogation":
		return c.derogate(ctx, bb, a)
	case "note":
		text, err := a.required("text")
		if err != nil {
			return nil, err
		}
		typ := a.str("target")
		if typ == "" {
			typ = "note"
		}
		data := map[string]any{}
		for k, v := range a.object("data") {
			data[k] = v
		}
		data["text"] = text
		items, err := c.p.Graph.AddItems(ctx, id, []domain.ChangeItem{{Type: domain.TypeArtifact, Target: typ, Status: domain.ItemProposed,
			Data: data, ProducedBy: producer(ctx)}})
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"item": items[0]})
	case "signal":
		typ, err := a.required("target")
		if err != nil {
			return nil, err
		}
		items, err := c.p.Graph.AddItems(ctx, id, []domain.ChangeItem{{Type: domain.TypeSignal, Target: typ, Status: domain.ItemProposed,
			Data: a.object("data"), Recipient: a.str("recipient"), ProducedBy: producer(ctx)}})
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
		out, err := c.p.Graph.ImpactNodeCreate(ctx, id, graphapi.NodeCreate{Key: key, Type: typ, Properties: a.object("properties"), Rationale: a.str("rationale"), ProducedBy: producer(ctx)})
		if err != nil {
			return nil, err
		}
		return nodeResult(out)
	case "edit":
		imp, err := w.touch(a.str("key"), a.str("rationale"))
		if err != nil {
			return nil, err
		}
		if err := w.expect(imp, a.object("expect")); err != nil {
			return nil, err
		}
		props, state := a.object("properties"), a.str("state")
		if len(props) == 0 && state == "" {
			return nil, errors.New(`edit "properties", or a "state" to move the node to`)
		}
		if len(props) > 0 {
			if state != "" {
				return nil, errors.New(`edit the properties and move the node to a state in two calls`)
			}
			return w.edit(imp, props, nil)
		}
		out, err := w.c.p.Graph.ImpactNodeTransition(ctx, id, graphapi.NodeTransition{NodeCheckout: graphapi.NodeCheckout{Impact: imp.ID}, To: state})
		if err != nil {
			return nil, err
		}
		return nodeResult(out)
	case "cancel":
		imp, ok := w.impact(a.str("key"))
		if !ok {
			return nil, fmt.Errorf("%s is not in the change", a.str("key"))
		}
		out, err := c.p.Graph.ImpactNodeCancel(ctx, id, imp.ID, "", "")
		if err != nil {
			return nil, err
		}
		return nodeResult(out)
	case "remove":
		keys := asStrings(a["keys"])
		if k := a.str("key"); k != "" {
			keys = append([]string{k}, keys...)
		}
		if len(keys) == 0 {
			return nil, errors.New(`argument "key" or "keys" is required`)
		}
		ids, removed := make([]domain.ChangeImpactID, 0, len(keys)), make([]string, 0, len(keys))
		for _, k := range keys {
			imp, ok := w.impact(k)
			if !ok {
				return nil, fmt.Errorf("%s is not in the change", k)
			}
			if slices.Contains(ids, imp.ID) {
				continue
			}
			ids, removed = append(ids, imp.ID), append(removed, imp.Key)
		}
		if len(ids) == 1 {
			if err := c.p.Graph.WithdrawImpact(ctx, id, ids[0], "", ""); err != nil {
				return nil, err
			}
			return map[string]any{"removed": removed[0]}, nil
		}
		// several at once: all of them or none (a5a144d)
		if err := c.p.Graph.WithdrawImpacts(ctx, id, ids, "", ""); err != nil {
			return nil, err
		}
		return map[string]any{"removed": removed}, nil
	case "merge", "split":
		return c.restructure(ctx, w, op, a)
	case "unlink":
		typ, to := a.str("type"), a.str("to")
		if typ == "" || to == "" {
			return nil, errors.New(`arguments "type" and "to" are required`)
		}
		rationale := a.str("rationale")
		if rationale == "" {
			rationale = "unlink " + typ + " to " + to
		}
		imp, err := w.touch(a.str("from"), rationale)
		if err != nil {
			return nil, err
		}
		if imp, err = w.checkout(imp); err != nil {
			return nil, err
		}
		target, ok, err := w.base(to)
		if err != nil {
			return nil, err
		}
		if t, found := w.impact(to); found && t.Post != nil {
			target.ID, ok = t.Post.ID, true
		}
		if !ok {
			return nil, fmt.Errorf("no node %s", to)
		}
		bb, err := w.c.p.Graph.BlackboardIn(ctx, id, "")
		if err != nil {
			return nil, err
		}
		for _, l := range bb.Nodes[*imp.Post].Out {
			if l.Type == typ && l.To.ID == target.ID {
				if err := w.c.p.Graph.ImpactLinkDelete(ctx, id, l.ID, "", ""); err != nil {
					return nil, err
				}
				return nodeResult(imp)
			}
		}
		return nil, fmt.Errorf("%s has no %s link to %s", imp.Key, typ, to)
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
		return w.edit(imp, nil, []graphapi.LinkWrite{{Type: typ, To: ref}})
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
		f, err := c.p.Graph.OpenOption(ctx, id, graphapi.OpenOptionRequest{Name: name, Hypothesis: a.str("hypothesis"), Activate: a.boolean("activate"), By: who.Subject})
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
// the policy of its point may leave to a person's ratification, which is not a tool.
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
		in := graphapi.OpenDecisionRequest{Question: a.str("question"), Criteria: strs("criteria"), By: who.Subject}
		if _, ok := a["options"]; ok {
			in.Options = append([]string{}, strs("options")...)
		}
		// the policy values the tool names are handed over as they come, the graph's policy reads them
		for _, k := range []string{decision.KeyDecider, decision.KeyThreshold, decision.KeyMaxRounds, decision.KeyMaxDuration} {
			if v, ok := a[k]; ok && v != "" && v != 0.0 {
				if in.Policy == nil {
					in.Policy = map[string]any{}
				}
				in.Policy[k] = v
			}
		}
		d, err = c.p.Graph.OpenDecision(ctx, id, in)
	case "rule":
		d, err = c.p.Graph.RuleDecision(ctx, id, graphapi.RuleRequest{Point: a.str("point"), Outcome: a.str("outcome"), Option: a.str("option"),
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
	title, intent, ns, parent := a.str("title"), a.str("intent"), a.str("namespace"), domain.ChangeID(a.str("parent"))
	if title == "" || intent == "" || (ns == "" && parent == "") {
		return nil, errors.New(`arguments "title", "intent" and "namespace" (or "parent") are required`)
	}
	// the free-form data of the change (ADR 0065), then what the platform stamps
	data := map[string]any{}
	for k, v := range a.object("data") {
		data[k] = v
	}
	if l := a.str("criticality"); l != "" {
		if !criticality.Valid(criticality.Level(l)) {
			return nil, fmt.Errorf("criticality %q: C1, C2 or C3", l)
		}
		data[domain.DataCriticality] = l
	}
	data["createdBy"], data["via"] = who.Subject, mcpbuiltin.Change
	if parent != "" {
		// a sub-change (ADR 0103): forked from its parent's branch, in its namespace and project
		p, err := c.p.Graph.Change(ctx, parent)
		if err != nil {
			return nil, err
		}
		ch, err := c.p.Graph.CreateChange(ctx, graphapi.NewChange{Title: title, Intent: intent, Methodology: a.str("methodology"), Namespace: p.Namespace,
			ParentID: parent, OwnerOrg: cmp.Or(a.str("unit"), p.OwnerOrg.String()), ProjectID: p.ProjectID.String(), Data: data})
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"change": changeSummary(ch)})
	}
	head, err := c.p.Graph.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		return nil, fmt.Errorf("head of main of %s: %w", ns, err)
	}
	// the change acts in the project the call names, else the caller's active project, else the root (ADR 0091)
	project := cmp.Or(a.str("project"), who.Project)
	if project == "" {
		st, err := c.p.Graph.Structures(ctx)
		if err != nil {
			return nil, err
		}
		project = st.Project().Root
	}
	ch, err := c.p.Graph.CreateChange(ctx, graphapi.NewChange{Title: title, Intent: intent, Methodology: a.str("methodology"), Namespace: ns,
		BaselineID: head.ID, OwnerOrg: a.unit(ctx, who), ProjectID: project, Data: data})
	if err != nil {
		return nil, err
	}
	return result(map[string]any{"change": changeSummary(ch)})
}

// close abandons an open change, with the reason in its data (closedBecause). From a process it reaches only the
// calling change and its sub-changes: a proof of concept concluded, a sub-change that commits nothing (ADR 0131).
func (c Change) close(ctx context.Context, id domain.ChangeID, a args) (map[string]any, error) {
	reason, err := a.required("reason")
	if err != nil {
		return nil, err
	}
	ch, err := c.p.Graph.Change(ctx, id)
	if err != nil {
		return nil, err
	}
	if from := domain.ChangeID(mcp.CallFrom(ctx).Change); from != "" && from != id && ch.ParentID != from {
		return nil, fmt.Errorf("change %s is neither the calling change nor one of its sub-changes", id)
	}
	abandoned := domain.ChangeAbandoned
	closed, err := c.p.Graph.UpdateChange(ctx, id, graphapi.ChangePatch{Status: &abandoned, Data: map[string]any{"closedBecause": reason}})
	if err != nil {
		return nil, err
	}
	return result(map[string]any{"change": changeSummary(closed)})
}

// intentItemType is the item type reformulate uses to keep the history of a change's definition:
// each revision supersedes the previous one (never overwritten), the header (title, intent) always
// holding the current one.
const intentItemType = "intent"

// latestIntentItem returns the current "intent" item of a change, if any has been recorded yet
// (none: the change is still on the definition it was created with).
func latestIntentItem(c domain.Change) *domain.ChangeItem {
	for i := len(c.Items) - 1; i >= 0; i-- {
		if c.Items[i].Type == domain.TypeArtifact && c.Items[i].Target == intentItemType {
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
		snap, err := c.p.Graph.AddItems(ctx, id, []domain.ChangeItem{{Type: domain.TypeArtifact, Target: intentItemType, Status: domain.ItemAccepted,
			Data: map[string]any{"title": bb.Change.Title, "intent": bb.Change.Intent}, ProducedBy: "create"}})
		if err != nil {
			return nil, err
		}
		prev = &snap[0]
	}
	items, err := c.p.Graph.AddItems(ctx, id, []domain.ChangeItem{{Type: domain.TypeArtifact, Target: intentItemType, Status: domain.ItemAccepted,
		Data: map[string]any{"title": title, "intent": newIntent, "rationale": rationale}, ProducedBy: producer(ctx), Supersedes: []domain.ItemID{prev.ID}}})
	if err != nil {
		return nil, err
	}
	ch, err := c.p.Graph.UpdateChange(ctx, id, graphapi.ChangePatch{Title: &title, Intent: &newIntent})
	if err != nil {
		return nil, err
	}
	return result(map[string]any{"change": changeSummary(ch), "item": items[0]})
}

// list lists changes matching a filter, the latest first: the caller checks whether a request
// continues one of them before opening a new one (goap-change.create).
func (c Change) list(ctx context.Context, a args) (map[string]any, error) {
	f := graphapi.ChangesFilter{Namespace: a.str("namespace"), OwnerOrg: a.str("unit")}
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

// gate applies the access gate of the platform to the access nodes (`adminOnly` node types, ADR 0068 and 0020): in
// process the connector writes through the graph directly, so the handler's gate does not stand in front of it.
func (c Change) gate(ctx context.Context, who authz.Principal, typ string) error {
	if adminOnly, err := c.p.Graph.AdminOnlyType(ctx, typ); err != nil {
		return err
	} else if !adminOnly {
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
	return mcpbuiltin.Change
}

func changeSummary(c domain.Change) map[string]any {
	return map[string]any{"id": c.ID, "title": c.Title, "intent": c.Intent, "methodology": c.Methodology, "methodologyVersion": c.MethodologyVersion, "namespace": c.Namespace,
		"unit": c.OwnerOrg, "status": c.Status, "baselineId": c.BaselineID, "branch": c.Branch, "parentId": c.ParentID, "project": c.ProjectID,
		"guardians": c.Guardians, "dependsOn": c.DependsOn}
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
	out, err := w.c.p.Graph.ProposeImpact(w.ctx, w.bb.Change.ID, []domain.ChangeImpact{imp})
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

// checkout returns the change impact with a draft of its own on the flow the call works on, the node checked out when
// it has none (ADR 0076, 0079).
func (w *working) checkout(imp domain.ChangeImpact) (domain.ChangeImpact, error) {
	if imp.Post != nil {
		if nv, ok := w.bb.Nodes[*imp.Post]; ok && graphapi.IsWorking(w.bb.Change, domain.ActiveOptionOf(w.bb), nv.Node) {
			return imp, nil
		}
	}
	return w.c.p.Graph.ImpactNodeCheckout(w.ctx, w.bb.Change.ID, graphapi.NodeCheckout{Impact: imp.ID})
}

// edit edits the draft of a change impact: its properties and outgoing links.
func (w *working) edit(imp domain.ChangeImpact, props map[string]any, links []graphapi.LinkWrite) (map[string]any, error) {
	imp, err := w.checkout(imp)
	if err != nil {
		return nil, err
	}
	if len(props) > 0 {
		if imp, err = w.c.p.Graph.ImpactNodeUpdate(w.ctx, w.bb.Change.ID, imp.ID, graphapi.NodeUpdate{Properties: props}); err != nil {
			return nil, err
		}
	}
	for _, l := range links {
		if _, err := w.c.p.Graph.ImpactLinkCreate(w.ctx, w.bb.Change.ID, imp.ID, l, "", ""); err != nil {
			return nil, err
		}
	}
	return nodeResult(imp)
}

// restructure merges or splits nodes from the side of their parents (ADR 0077): the sources are named by key, the
// graph finds their parents; the gate of the access nodes stands in front of every node the call modifies.
func (c Change) restructure(ctx context.Context, w *working, op string, a args) (map[string]any, error) {
	id, who := w.bb.Change.ID, w.who
	gate := func(typ string) error { return c.gate(ctx, who, typ) }
	create := func(m map[string]any) graphapi.NodeCreate {
		s := args(m)
		props, _ := m["properties"].(map[string]any)
		return graphapi.NodeCreate{Key: s.str("key"), Type: s.str("type"), Properties: props, Rationale: s.str("rationale"), ProducedBy: producer(ctx)}
	}
	var out graphapi.Restructured
	var err error
	switch op {
	case "merge":
		var sources []graphapi.NodeName
		for _, x := range asStrings(a["sources"]) {
			sources = append(sources, graphapi.NodeName{Key: x})
		}
		out, err = c.p.Graph.ImpactNodeMerge(ctx, id, graphapi.MergeInput{Sources: sources, Gate: gate,
			Into: create(map[string]any{"key": a.str("key"), "type": a.str("type"), "properties": a.object("properties"), "rationale": a.str("rationale")})})
	default:
		var into []graphapi.NodeCreate
		raw, _ := a["into"].([]any)
		for _, x := range raw {
			if m, ok := x.(map[string]any); ok {
				into = append(into, create(m))
			}
		}
		out, err = c.p.Graph.ImpactNodeSplit(ctx, id, graphapi.SplitInput{Source: graphapi.NodeName{Key: a.str("source")}, Into: into, Gate: gate,
			Rationale: a.str("rationale")})
	}
	if err != nil {
		return nil, err
	}
	summary := func(list []domain.ChangeImpact) []map[string]any {
		var rows []map[string]any
		for _, cn := range list {
			rows = append(rows, map[string]any{"key": cn.Key, "type": cn.Type, "intent": cn.Intent, "review": cn.Review, "post": cn.Post, "via": cn.Via})
		}
		return rows
	}
	return result(map[string]any{"successors": summary(out.Successors), "sources": summary(out.Sources), "parents": summary(out.Parents), "suspect": out.Suspect})
}

func asStrings(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

func nodeResult(out domain.ChangeImpact) (map[string]any, error) {
	return result(map[string]any{"node": map[string]any{"key": out.Key, "type": out.Type, "intent": out.Intent, "review": out.Review, "post": out.Post}})
}

// record raises or updates a risk or an action of the change (ADR 0036 §1): a new version of the record of its key,
// keeping what it does not restate. Without a key, a new record gets the next free one (RSK-n, ACT-n).
func (c Change) record(ctx context.Context, bb domain.Blackboard, op string, a args) (map[string]any, error) {
	kind, prefix, fields := risk.TypeRisk, "RSK-", []string{"title", "description", "status", "owner"}
	if op == "action" {
		kind, prefix, fields = risk.TypeAction, "ACT-", []string{"title", "status", "owner", "due", "for", "result", "kind", "purpose"}
	}
	key := a.str("key")
	var existing bool
	n := 0
	for _, it := range bb.Change.Items {
		if it.Type != kind {
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
	if kind == risk.TypeRisk {
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
	} else {
		// a proof of concept or a prototype (ADR 0131): what it checks, the sub-change carrying it
		for _, f := range []string{"risks", "hypotheses"} {
			if l, ok := a[f].([]any); ok {
				data[f] = l
			}
		}
		if sub := a.str("subChange"); sub != "" {
			data["change"] = sub
		}
	}
	if !existing && a.str("title") == "" {
		return nil, fmt.Errorf("a new %s needs a title", op)
	}
	if existing {
		// the title is required on every version: keep the current one
		for _, it := range slices.Backward(bb.Change.Items) {
			if k, _ := it.Data["key"].(string); it.Type == kind && k == key {
				if _, ok := data["title"]; !ok {
					data["title"] = it.Data["title"]
				}
				break
			}
		}
	}
	items, err := c.p.Graph.AddItems(ctx, bb.Change.ID, []domain.ChangeItem{{Type: kind, Target: op, Status: domain.ItemProposed, Data: data, ProducedBy: producer(ctx)}})
	if err != nil {
		return nil, err
	}
	return result(map[string]any{op: items[0]})
}

// derogate signs a derogation of the change, or writes the next version of one (ADR 0075 §2): the signatory is the
// caller, the graph checks derogation:sign (Graph.ItemAuthorizer); a version restates what the previous one said.
func (c Change) derogate(ctx context.Context, bb domain.Blackboard, a args) (map[string]any, error) {
	key := a.str("key")
	n := 0
	for _, d := range risk.Derogations(bb.Change) {
		var i int
		if _, err := fmt.Sscanf(d.Key, "DRG-%d", &i); err == nil && i > n {
			n = i
		}
	}
	data := map[string]any{}
	if key == "" {
		key = fmt.Sprintf("DRG-%d", n+1)
	} else {
		for _, d := range risk.Derogations(bb.Change) {
			if d.Key == key {
				data = map[string]any{"rule": d.Rule, "target": d.Target, "reason": d.Reason, "expires": d.Expires.Format(time.RFC3339), "status": d.Status}
			}
		}
	}
	data["key"], data["signatory"] = key, authz.From(ctx).Subject
	for _, f := range []string{"rule", "target", "reason", "expires", "status"} {
		if v := a.str(f); v != "" {
			data[f] = v
		}
	}
	items, err := c.p.Graph.AddItems(ctx, bb.Change.ID, []domain.ChangeItem{{Type: risk.TypeDerogation, Target: "derogation", Status: domain.ItemProposed, Data: data, ProducedBy: producer(ctx)}})
	if err != nil {
		return nil, err
	}
	return result(map[string]any{"derogation": items[0]})
}

// tag names one exact version of a node (ADR 0143 §6): the one at the head of main unless version names another.
func (c Change) tag(ctx context.Context, a args) (map[string]any, error) {
	key, err := a.required("key")
	if err != nil {
		return nil, err
	}
	name, err := a.required("name")
	if err != nil {
		return nil, err
	}
	v, err := Graph(c).view(ctx, args{"namespace": a.str("namespace")})
	if err != nil {
		return nil, err
	}
	n, ok := v.node(key)
	if !ok {
		return nil, fmt.Errorf("no node %s in %s", key, a.str("namespace"))
	}
	ref := n.Ref()
	switch ver := a["version"].(type) {
	case float64:
		if ver > 0 {
			ref.Version = domain.Version(ver)
		}
	case int:
		if ver > 0 {
			ref.Version = domain.Version(ver)
		}
	}
	t, err := c.p.Graph.TagVersion(ctx, ref, name)
	if err != nil {
		return nil, err
	}
	return result(map[string]any{"tag": map[string]any{"id": t.ID, "name": t.Name, "key": n.Key, "version": t.Version}})
}
