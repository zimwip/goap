package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/mcp"
)

// Graph is the goap-graph connector: reads of the versioned graph, as of the head of the main branch
// of a namespace or of a given baseline. Nodes of a type the caller may not read are left out.
type Graph struct{ p Ports }

var _ connectorkit.Connector = Graph{}

var graphOps = []op{
	{"read", "Read a node by key: {node, out, in}", schema(map[string]string{"namespace": "string", "key": "string", "baseline": "string"}, "namespace", "key")},
	{"glob", "List the nodes whose key matches a glob: {nodes, truncated}", schema(map[string]string{"namespace": "string", "pattern": "string", "type": "string", "baseline": "string", "limit": "integer"}, "namespace")},
	{"grep", "Search the nodes whose properties match a regular expression: {nodes, truncated}", schema(map[string]string{"namespace": "string", "pattern": "string", "type": "string", "property": "string", "ignoreCase": "boolean", "baseline": "string", "limit": "integer"}, "namespace", "pattern")},
	{"links", "List the links of a node: {links}", schema(map[string]string{"namespace": "string", "key": "string", "direction": "string", "type": "string", "baseline": "string"}, "namespace", "key")},
	{"baselines", "List the baselines of a namespace: {head, baselines}", schema(map[string]string{"namespace": "string", "limit": "integer"}, "namespace")},
}

// Info implements connectorkit.Connector.
func (Graph) Info() *connectorv1.ConnectorInfo {
	return info(mcp.BuiltinGraph, "The versioned graph of the platform, read for the caller (built in).", graphOps)
}

// view is the graph of a baseline as the caller may see it.
type view struct {
	baseline domain.BaselineID
	nodes    []domain.Node
	byRef    map[domain.NodeRef]domain.Node
	links    []domain.Link
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

// view reads the graph of the baseline the call names, else of the head of main of the namespace.
func (c Graph) view(ctx context.Context, a args) (*view, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := a.required("namespace")
	if err != nil {
		return nil, err
	}
	id := domain.BaselineID(a.str("baseline"))
	if id == "" {
		head, err := c.p.Graph.BranchHead(ctx, ns, domain.MainBranch)
		if err != nil {
			return nil, fmt.Errorf("head of main of %s: %w", ns, err)
		}
		id = head.ID
	}
	nodes, links, err := c.p.Graph.BaselineGraph(ctx, id)
	if err != nil {
		return nil, err
	}
	r := &readable{ctx: ctx, a: c.p.Authz, who: who, seen: map[string]bool{}}
	v := &view{baseline: id, byRef: map[domain.NodeRef]domain.Node{}}
	for _, n := range nodes {
		if n.Namespace == ns && !n.Deleted && r.ok(n.Namespace, n.Type) {
			v.nodes = append(v.nodes, n)
			v.byRef[n.Ref()] = n
		}
	}
	sort.Slice(v.nodes, func(i, j int) bool { return v.nodes[i].Key < v.nodes[j].Key })
	for _, l := range links {
		_, from := v.byRef[l.From]
		_, to := v.byRef[l.To]
		if from || to {
			v.links = append(v.links, l)
		}
	}
	return v, nil
}

func (v *view) node(key string) (domain.Node, bool) {
	for _, n := range v.nodes {
		if n.Key == key {
			return n, true
		}
	}
	return domain.Node{}, false
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

// linkView is a link seen from one of its ends: the type and the key and type of the other end.
func (v *view) linkView(l domain.Link, from bool) map[string]any {
	other := l.To
	if !from {
		other = l.From
	}
	out := map[string]any{"type": l.Type}
	if n, ok := v.byRef[other]; ok {
		out["key"], out["nodeType"] = n.Key, n.Type
	} else {
		out["id"] = other.ID // another namespace, or a type the caller may not read
	}
	if len(l.Properties) > 0 {
		out["properties"] = l.Properties
	}
	return out
}

func (v *view) linksOf(n domain.Node, direction, typ string) (out, in []map[string]any) {
	out, in = []map[string]any{}, []map[string]any{}
	for _, l := range v.links {
		if typ != "" && l.Type != typ {
			continue
		}
		if l.From == n.Ref() && direction != "in" {
			out = append(out, v.linkView(l, true))
		}
		if l.To == n.Ref() && direction != "out" {
			in = append(in, v.linkView(l, false))
		}
	}
	return out, in
}

// Invoke implements connectorkit.Connector.
func (c Graph) Invoke(ctx context.Context, op string, raw, _ map[string]any, _ map[string]string) (map[string]any, error) {
	a := args(raw)
	if op == "baselines" {
		return c.baselines(ctx, a)
	}
	v, err := c.view(ctx, a)
	if err != nil {
		return nil, err
	}
	switch op {
	case "read":
		key, err := a.required("key")
		if err != nil {
			return nil, err
		}
		n, ok := v.node(key)
		if !ok {
			return nil, fmt.Errorf("no node %s in %s (baseline %s)", key, a.str("namespace"), v.baseline)
		}
		out, in := v.linksOf(n, "", "")
		return result(map[string]any{"baseline": v.baseline, "node": map[string]any{"key": n.Key, "type": n.Type, "version": n.Version,
			"state": n.State, "properties": n.Properties}, "out": out, "in": in})
	case "glob":
		pattern := a.str("pattern")
		if pattern == "" {
			pattern = "*"
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf("pattern %q: %w", pattern, err)
		}
		var hits []map[string]any
		for _, n := range v.nodes {
			if ok, _ := path.Match(pattern, n.Key); ok && (a.str("type") == "" || n.Type == a.str("type")) {
				hits = append(hits, summary(n))
			}
		}
		return limited(v, hits, a.limit())
	case "grep":
		expr, err := a.required("pattern")
		if err != nil {
			return nil, err
		}
		if a.boolean("ignoreCase") {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("pattern: %w", err)
		}
		var hits []map[string]any
		for _, n := range v.nodes {
			if a.str("type") != "" && n.Type != a.str("type") {
				continue
			}
			matches := map[string]any{}
			for name, val := range n.Properties {
				if p := a.str("property"); p != "" && name != p {
					continue
				}
				if s := text(val); re.MatchString(s) {
					matches[name] = s
				}
			}
			if len(matches) > 0 {
				h := summary(n)
				h["matches"] = matches
				hits = append(hits, h)
			}
		}
		return limited(v, hits, a.limit())
	case "links":
		key, err := a.required("key")
		if err != nil {
			return nil, err
		}
		n, ok := v.node(key)
		if !ok {
			return nil, fmt.Errorf("no node %s in %s (baseline %s)", key, a.str("namespace"), v.baseline)
		}
		out, in := v.linksOf(n, a.str("direction"), a.str("type"))
		return result(map[string]any{"baseline": v.baseline, "out": out, "in": in})
	}
	return nil, unknown(op)
}

func limited(v *view, hits []map[string]any, limit int) (map[string]any, error) {
	if hits == nil {
		hits = []map[string]any{}
	}
	truncated := len(hits) > limit
	if truncated {
		hits = hits[:limit]
	}
	return result(map[string]any{"baseline": v.baseline, "nodes": hits, "truncated": truncated})
}

// text is the searchable text of a property value.
func text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
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
