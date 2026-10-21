package domain

import (
	"fmt"
	"strings"

	"github.com/zimwip/goap/contracts/lnk"
)

// SourceName is the source of the node (ADR 0154): the one it was read from (Source, set by a resolver reading
// another graph), else the node's own namespace, a source of this graph. Every working node has one.
func (n Node) SourceName() string {
	if n.Source != "" {
		return n.Source
	}
	return NamespaceOf(n.Namespace)
}

// URL is the logical URL of the node: lnk:<source>/<type>/<key>, the type named in its domain (the namespace is the
// source's). It names the node whatever its version (URL().At(v) pins one).
func (n Node) URL() lnk.URL { return lnk.New(n.SourceName(), lnk.BareType(n.Type), n.Key) }

// Target is the node as a target of a logical URL (ADR 0154): what a resolver answers for it.
func (n Node) Target() lnk.Target {
	t := lnk.Target{URL: n.URL(), Kind: lnk.TargetNode, Type: n.Type, Key: n.Key, Properties: n.Properties,
		Node: &lnk.NodeRef{ID: string(n.ID), Version: int(n.Version)}}
	if s, ok := n.Properties["name"].(string); ok {
		t.Title = s
	} else if s, ok := n.Properties["title"].(string); ok {
		t.Title = s
	}
	return t
}

// TypeSource is the node type of a declared source (ADR 0154), a node of the platform namespace keyed
// SourceKeyPrefix + its name: where the things of the source live, and the kind of resolver that reads them.
const (
	TypeSource      = "platform@Source"
	SourceKeyPrefix = "SRC:"
)

// SourceKey is the key of the node declaring a source.
func SourceKey(name string) string { return SourceKeyPrefix + name }

// SourceOf reads the source a platform@Source node declares (its name is its key's, a property name differing from it
// is an error).
func SourceOf(n Node) (lnk.Source, error) {
	str := func(k string) string { s, _ := n.Properties[k].(string); return s }
	name, ok := strings.CutPrefix(n.Key, SourceKeyPrefix)
	if !ok {
		return lnk.Source{}, fmt.Errorf("source node %q: want key %s<name>: %w", n.Key, SourceKeyPrefix, lnk.ErrInvalid)
	}
	if p := str("name"); p != "" && p != name {
		return lnk.Source{}, fmt.Errorf("source node %q names %q: %w", n.Key, p, lnk.ErrInvalid)
	}
	src := lnk.Source{Name: name, Kind: str("kind"), Namespace: str("namespace"), Location: str("location"),
		Branch: str("branch"), Description: str("description")}
	switch ns := n.Properties["namespaces"].(type) {
	case []string:
		src.Namespaces = ns
	case []any:
		for _, v := range ns {
			if s, ok := v.(string); ok && s != "" {
				src.Namespaces = append(src.Namespaces, s)
			}
		}
	}
	if p, ok := n.Properties["params"].(map[string]any); ok {
		src.Params = p
	}
	return src, src.Validate()
}
