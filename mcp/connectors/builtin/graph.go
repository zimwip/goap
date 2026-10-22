package builtin

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"sort"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	connectorv1 "github.com/zimwip/goap/contracts/gen/goap/connector/v1"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/contracts/mcp"
	"github.com/zimwip/goap/lib/mcpbuiltin"
	"github.com/zimwip/goap/mcp/connectorkit"
)

// Graph is the goap-graph connector: reads of the versioned graph in a view (ADR 0157): the change the call names or
// the calling process works on when it acts on the namespace, else a lasting branch at a baseline (default: the head of
// main). It reads node by node and page by page, never a namespace whole. Nodes of a type the caller may not read are
// left out.
type Graph struct{ p Ports }

var _ connectorkit.Connector = Graph{}

// viewArgs are the arguments naming the view of a read.
var viewArgs = map[string]string{"baseline": "string", "branch": "string", "change": "string", "flow": "string"}

func withView(props map[string]string) map[string]string {
	out := maps.Clone(props)
	maps.Copy(out, viewArgs)
	return out
}

var graphOps = []op{
	{"read", "Read a node by key: {view, node, out, in}", schema(withView(map[string]string{"namespace": "string", "key": "string"}), "namespace", "key")},
	{"glob", "List the nodes whose key matches a glob: {view, nodes, truncated}", schema(withView(map[string]string{"namespace": "string", "pattern": "string", "type": "string", "limit": "integer"}), "namespace")},
	{"grep", "Search the nodes whose properties match a regular expression: {view, nodes, truncated}", schema(withView(map[string]string{"namespace": "string", "pattern": "string", "type": "string", "property": "string", "ignoreCase": "boolean", "limit": "integer"}), "namespace", "pattern")},
	{"links", "List the links of a node: {view, links}", schema(withView(map[string]string{"namespace": "string", "key": "string", "direction": "string", "type": "string"}), "namespace", "key")},
	{"baselines", "List the baselines of a namespace: {head, baselines}", schema(map[string]string{"namespace": "string", "limit": "integer"}, "namespace")},
	{"tags", "List the tags of a node, or the versions a tag name labels: {tags}", schema(withView(map[string]string{"namespace": "string", "key": "string", "name": "string"}), "namespace")},
}

// maxScan caps the nodes a glob or a grep looks at: past it the answer is truncated.
const maxScan = 5000

// Info implements connectorkit.Connector.
func (Graph) Info() *connectorv1.ConnectorInfo {
	return info(mcpbuiltin.Graph, "The versioned graph of the platform, read for the caller (built in).", graphOps)
}

// reader reads one view for the caller.
type reader struct {
	c    Graph
	ctx  context.Context
	ns   string
	view graphapi.View
	r    *readable
}

// readable filters the nodes by the read authorization of their type (decided once per type).
type readable struct {
	ctx  context.Context
	a    authz.Authorizer
	who  authz.Principal
	seen map[string]bool
}

func (r *readable) ok(ns, typ string) bool {
	if r.a == nil {
		return true
	}
	if v, done := r.seen[typ]; done {
		return v
	}
	ok, err := r.a.Authorize(r.ctx, authz.Request{Subject: r.who, Action: "read", Resource: authz.Resource{Type: typ, Namespace: ns}})
	r.seen[typ] = err == nil && ok
	return r.seen[typ]
}

// reader opens the view the call names: the change named, else the calling process's, when it acts on the namespace;
// else the branch named (default main) at the baseline named (default its head).
func (c Graph) reader(ctx context.Context, a args) (*reader, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := a.required("namespace")
	if err != nil {
		return nil, err
	}
	v := graphapi.View{Namespace: ns, Branch: cmp.Or(a.str("branch"), domain.MainBranch), Baseline: domain.BaselineID(a.str("baseline"))}
	id, named, flow := a.str("change"), a.str("change") != "", a.str("flow")
	if call := mcp.CallFrom(ctx); !named && a.str("branch") == "" && a.str("baseline") == "" {
		id, flow = call.Change, cmp.Or(flow, call.Flow)
	}
	if id != "" {
		ch, err := c.p.Graph.Change(ctx, domain.ChangeID(id))
		if err != nil {
			return nil, err
		}
		switch {
		case ch.Namespace == ns:
			v = graphapi.View{Change: ch.ID, Flow: flow}
		case named:
			return nil, fmt.Errorf("change %s acts on %s, not on %s: %w", id, ch.Namespace, ns, graphapi.ErrInvalid)
		}
	}
	return &reader{c: c, ctx: ctx, ns: ns, view: v, r: &readable{ctx: ctx, a: c.p.Authz, who: who, seen: map[string]bool{}}}, nil
}

func (r *reader) readable(n domain.Node) bool {
	return !n.Deleted && r.r.ok(n.Namespace, n.Type)
}

// node reads a node of the view by key.
func (r *reader) node(key string) (domain.NodeView, error) {
	nv, err := r.c.p.Graph.ViewNode(r.ctx, r.view, "", key)
	if err == nil && !r.readable(nv.Node) {
		err = graphapi.ErrNotFound
	}
	if err != nil {
		return domain.NodeView{}, fmt.Errorf("no node %s in %s: %w", key, r.view, err)
	}
	return nv, nil
}

// scan pages through the nodes of the view matching a type and a text (a superset of what the caller keeps), keeping
// those keep accepts, up to limit (truncated past it, or past maxScan nodes looked at).
func (r *reader) scan(typ, text string, limit int, keep func(domain.Node) (map[string]any, bool)) ([]map[string]any, bool, error) {
	hits := []map[string]any{}
	for offset := 0; offset < maxScan; offset += graphapi.MaxBrowseLimit {
		page, err := r.c.p.Graph.ViewNodes(r.ctx, r.view, graphapi.NodeQuery{Type: typ, Text: text, Offset: offset, Limit: graphapi.MaxBrowseLimit})
		if err != nil {
			return nil, false, err
		}
		for _, n := range page.Nodes {
			if !r.readable(n) {
				continue
			}
			if h, ok := keep(n); ok {
				if len(hits) == limit {
					return hits, true, nil
				}
				hits = append(hits, h)
			}
		}
		if offset+len(page.Nodes) >= page.Total || len(page.Nodes) == 0 {
			return hits, false, nil
		}
	}
	return hits, true, nil
}

func summary(n domain.Node) map[string]any {
	out := map[string]any{"key": n.Key, "type": n.Type, "version": n.Version}
	if n.State != "" {
		out["state"] = n.State
	}
	for _, p := range []string{"title", "name"} {
		if t, ok := n.Properties[p]; ok {
			out[p] = t
		}
	}
	return out
}

// links reads the links of a node, seen from it: the type and the key and type of the other end.
func (r *reader) links(n domain.Node, direction, typ string) (out, in []map[string]any, err error) {
	nb, err := r.c.p.Graph.ViewNeighbourhood(r.ctx, r.view, n.ID)
	if err != nil {
		return nil, nil, err
	}
	byID := map[domain.NodeID]domain.Node{}
	for _, m := range nb.Nodes {
		byID[m.ID] = m
	}
	view := func(l domain.Link, other domain.NodeID) map[string]any {
		v := map[string]any{"type": l.Type}
		if m, ok := byID[other]; ok && m.Namespace == r.ns && r.readable(m) {
			v["key"], v["nodeType"] = m.Key, m.Type
		} else if l.External() {
			v["target"] = l.Target.String()
		} else {
			v["id"] = other // another namespace, or a type the caller may not read
		}
		if len(l.Properties) > 0 {
			v["properties"] = l.Properties
		}
		return v
	}
	out, in = []map[string]any{}, []map[string]any{}
	for _, l := range nb.Links {
		if typ != "" && l.Type != typ {
			continue
		}
		if l.From.ID == n.ID && direction != "in" {
			out = append(out, view(l, l.To.ID))
		}
		if l.To.ID == n.ID && direction != "out" {
			in = append(in, view(l, l.From.ID))
		}
	}
	return out, in, nil
}

// globText is the longest literal run of a glob: the text filter of the scan (a superset of the keys matching it).
func globText(pattern string) string {
	best := ""
	for _, part := range regexp.MustCompile(`[*?\\[\]]+`).Split(pattern, -1) {
		if len(part) > len(best) {
			best = part
		}
	}
	return best
}

// Invoke implements connectorkit.Connector.
func (c Graph) Invoke(ctx context.Context, op string, raw, _ map[string]any, _ map[string]string) (map[string]any, error) {
	a := args(raw)
	if op == "baselines" {
		return c.baselines(ctx, a)
	}
	r, err := c.reader(ctx, a)
	if err != nil {
		return nil, err
	}
	switch op {
	case "tags":
		return c.tags(r, a)
	case "read", "links":
		key, err := a.required("key")
		if err != nil {
			return nil, err
		}
		nv, err := r.node(key)
		if err != nil {
			return nil, err
		}
		direction, typ := a.str("direction"), a.str("type")
		if op == "read" {
			direction, typ = "", ""
		}
		out, in, err := r.links(nv.Node, direction, typ)
		if err != nil {
			return nil, err
		}
		res := map[string]any{"view": r.view.String(), "out": out, "in": in}
		if op == "read" {
			n := nv.Node
			res["node"] = map[string]any{"key": n.Key, "type": n.Type, "version": n.Version, "state": n.State, "properties": n.Properties}
		}
		return result(res)
	case "glob":
		pattern := cmp.Or(a.str("pattern"), "*")
		if _, err := path.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf("pattern %q: %w", pattern, err)
		}
		hits, truncated, err := r.scan(a.str("type"), globText(pattern), a.limit(), func(n domain.Node) (map[string]any, bool) {
			ok, _ := path.Match(pattern, n.Key)
			return summary(n), ok
		})
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"view": r.view.String(), "nodes": hits, "truncated": truncated})
	case "grep":
		expr, err := a.required("pattern")
		if err != nil {
			return nil, err
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("pattern: %w", err)
		}
		text, _ := re.LiteralPrefix()
		if a.boolean("ignoreCase") {
			re = regexp.MustCompile("(?i)" + expr)
		}
		hits, truncated, err := r.scan(a.str("type"), text, a.limit(), func(n domain.Node) (map[string]any, bool) {
			matches := map[string]any{}
			for name, val := range n.Properties {
				if p := a.str("property"); p != "" && name != p {
					continue
				}
				if s := propText(val); re.MatchString(s) {
					matches[name] = s
				}
			}
			h := summary(n)
			h["matches"] = matches
			return h, len(matches) > 0
		})
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"view": r.view.String(), "nodes": hits, "truncated": truncated})
	}
	return nil, unknown(op)
}

// propText is the searchable text of a property value.
func propText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// tags lists the tags of a node (key) or of a name, each with the key of the node it names when the view holds it and
// the caller reads it (ADR 0143 §6).
func (c Graph) tags(r *reader, a args) (map[string]any, error) {
	f := domain.TagFilter{Namespace: a.str("namespace"), Name: a.str("name")}
	keys := map[domain.NodeID]domain.Node{}
	if key := a.str("key"); key != "" {
		nv, err := r.node(key)
		if err != nil {
			return nil, err
		}
		f.Node, keys[nv.ID] = nv.ID, nv.Node
	} else if f.Name == "" {
		return nil, fmt.Errorf(`argument "key" or "name" is required`)
	}
	ts, err := c.p.Graph.Tags(r.ctx, f)
	if err != nil {
		return nil, err
	}
	list := []map[string]any{}
	for _, t := range ts {
		n, ok := keys[t.Node]
		if !ok {
			nv, err := c.p.Graph.ViewNode(r.ctx, r.view, t.Node, "")
			if err != nil || !r.readable(nv.Node) {
				continue // a node the view does not hold, or the caller may not read
			}
			n, keys[t.Node] = nv.Node, nv.Node
		}
		list = append(list, map[string]any{"name": t.Name, "key": n.Key, "type": n.Type, "version": t.Version, "by": t.By, "createdAt": t.CreatedAt})
	}
	return result(map[string]any{"tags": list})
}

func (c Graph) baselines(ctx context.Context, a args) (map[string]any, error) {
	if _, err := caller(ctx); err != nil {
		return nil, err
	}
	ns, err := a.required("namespace")
	if err != nil {
		return nil, err
	}
	bs, err := c.p.Graph.Baselines(ctx, ns)
	if err != nil {
		return nil, err
	}
	sort.Slice(bs, func(i, j int) bool { return bs[i].CreatedAt.After(bs[j].CreatedAt) })
	out := map[string]any{}
	if head, err := c.p.Graph.BranchHead(ctx, ns, domain.MainBranch); err == nil {
		out["head"] = head.ID
	}
	list := []map[string]any{}
	for i, b := range bs {
		if i == a.limit() {
			break
		}
		list = append(list, map[string]any{"id": b.ID, "name": b.Name, "createdAt": b.CreatedAt})
	}
	out["baselines"] = list
	return result(out)
}
