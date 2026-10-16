package domain

import "slices"

// Hierarchy is one structure of the graph read as a tree of node keys (ADR 0054, 0068): the child -> parent links of
// the organisation or of the projects, with the root every chain ends with. It is the one place that walks them:
// pkg/access (roles), internal/mcpsvc (adapters) and the goap-admin listings resolve a chain through it.
type Hierarchy struct {
	root   string
	parent map[string]string
}

// NewHierarchy returns an empty hierarchy whose chains end with root.
func NewHierarchy(root string) *Hierarchy {
	return &Hierarchy{root: root, parent: map[string]string{}}
}

// Hierarchy reads the structure of a kind from the nodes and links of the graph: the parent links the structure
// names, between nodes of its types (the tagged type and its subtypes), a self link (the root project) never
// being a parent. Nodes outside the structure and links of other types are ignored.
func (s Structures) Hierarchy(kind string, nodes []Node, links []Link) *Hierarchy {
	st := s.Of(kind)
	h := NewHierarchy(st.Root)
	byID := make(map[NodeID]Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	for _, l := range links {
		from, to := byID[l.From.ID], byID[l.To.ID]
		if l.Type == st.Parent && s.In(kind, from.Type) && s.In(kind, to.Type) {
			h.Add(from.Key, to.Key)
		}
	}
	return h
}

// Add records the parent of a node; a self link is no parent.
func (h *Hierarchy) Add(child, parent string) {
	if child != parent {
		h.parent[child] = parent
	}
}

// Root is the key every chain ends with.
func (h *Hierarchy) Root() string { return h.root }

// Parent is the node a node is part of; empty when it has none (the root, or a node not placed).
func (h *Hierarchy) Parent(key string) string { return h.parent[key] }

// Chain returns a node followed by its ancestors, nearest first, ending with the root (appended when the walk did
// not reach it: a node with no parent link inherits from the root). An empty key stands for the root. The walk stops
// at a cycle.
func (h *Hierarchy) Chain(key string) []string {
	if key == "" {
		key = h.root
	}
	out := []string{key}
	for cur := key; ; {
		p, ok := h.parent[cur]
		if !ok || p == "" || slices.Contains(out, p) {
			break
		}
		out = append(out, p)
		cur = p
	}
	if !slices.Contains(out, h.root) {
		out = append(out, h.root)
	}
	return out
}
