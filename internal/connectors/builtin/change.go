package builtin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
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

// impact returns the change impact of a node on the main flow.
func (w *working) impact(key string) (domain.ChangeImpact, bool) {
	for _, n := range w.bb.Change.Nodes {
		if n.Key == key && n.Flow == "" && !n.Superseded {
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
