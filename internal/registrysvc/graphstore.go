package registrysvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/graphsnap"
)

// TypeMethodologyVersion is the type of the header node of a methodology version (built-in meta-domain, ADR 0023): its
// scalar fields, status and timestamps; one node per element of the definition (see defs.go), tied by "defines" links.
const TypeMethodologyVersion = "methodology@MethodologyVersion"

// StoreGraph is what the store needs from the graph (*graph.Graph and the graph service client implement it).
type StoreGraph interface {
	BranchHead(ctx context.Context, name string) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
	CreateBaseline(ctx context.Context, name string, nodes []domain.NodeRef) (domain.Baseline, error)
	// Commit runs a change of node edits (ADR 0024).
	Commit(ctx context.Context, in graph.Commit) (graph.CommitResult, error)
}

// linkDefines is the link type that ties a methodology version to the elements of its definition.
const linkDefines = "methodology@defines"

// statusDeleted marks a deleted draft: a node key is never freed on a versioned graph, so the node stays and a later
// Save of the same version revives it. The stores treat it as absent.
const statusDeleted Status = "deleted"

// NamespaceMethodology is the namespace of the methodology versions and their elements. The registry API is what callers
// use; the graph holds the content.
const NamespaceMethodology = "methodology"

// MethodologyVersionKey is the key of the node of a methodology version.
func MethodologyVersionKey(name, version string) string { return "MV:" + key(name, version) }

// GraphStore keeps methodology versions as nodes of the graph, changed through changes applied on main: they are
// versioned, journaled and reviewable like any node (each element has its own history). It implements Store; the domains
// are in a DomainStore of the registry (ADR 0023).
type GraphStore struct {
	Graph StoreGraph

	cache graphsnap.Cache[*defs]
}

var _ Store = (*GraphStore)(nil)

// NewGraphStore returns a store over a graph (the graph itself, or a client of the graph service).
func NewGraphStore(g StoreGraph) *GraphStore {
	s := &GraphStore{Graph: g}
	s.cache = graphsnap.Cache[*defs]{Graph: g, Build: buildDefs}
	return s
}

// defs is the versions of a baseline, decoded once per head of main.
type defs struct {
	methodologies map[string]stored[Record]
	problems      []string
}

// stored is a decoded version with the nodes it comes from: the header and every element node (removed ones too, so that
// an element deleted from a draft and added again is revived, not created twice).
type stored[T any] struct {
	node     domain.Node
	rec      T
	children map[string]domain.Node // by key
}

var metaKeys = []string{"status", "createdAt", "updatedAt", "publishedAt", "updatedBy"}

func kindOfType(t string) string {
	for _, k := range defKinds {
		if k.nodeType == t {
			return k.kind
		}
	}
	return ""
}

func buildDefs(_ domain.BaselineID, nodes []domain.Node, _ []domain.Link) *defs {
	d := &defs{methodologies: map[string]stored[Record]{}}
	type owner struct{ ns, key string }
	children := map[owner]map[string]domain.Node{} // header -> element nodes
	for _, n := range nodes {
		if kindOfType(n.Type) == "" {
			continue
		}
		parent, _, _ := strings.Cut(n.Key, "/")
		o := owner{n.Namespace, parent}
		if children[o] == nil {
			children[o] = map[string]domain.Node{}
		}
		children[o][n.Key] = n
	}
	for _, n := range nodes {
		if n.Namespace != NamespaceMethodology || n.Type != TypeMethodologyVersion {
			continue
		}
		var r Record
		if err := decodeVersion(n, children[owner{n.Namespace, n.Key}], &r.Methodology, &r.Status, &r.CreatedAt, &r.UpdatedAt, &r.PublishedAt, &r.UpdatedBy); err != nil {
			d.problems = append(d.problems, fmt.Sprintf("%s: %v", n.Key, err))
			continue
		}
		d.methodologies[key(r.Methodology.Name, r.Methodology.Version)] = stored[Record]{n, r, children[owner{n.Namespace, n.Key}]}
	}
	return d
}

// decodeVersion assembles a definition from its header node and its live element nodes.
func decodeVersion(n domain.Node, elements map[string]domain.Node, def any, status *Status, created, updated, published *time.Time, by *string) error {
	props := maps.Clone(n.Properties)
	s, _ := props["status"].(string)
	*status = Status(s)
	*by, _ = props["updatedBy"].(string)
	for k, dst := range map[string]*time.Time{"createdAt": created, "updatedAt": updated, "publishedAt": published} {
		if s, _ := props[k].(string); s != "" {
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			*dst = t
		}
	}
	for _, k := range metaKeys {
		delete(props, k)
	}
	var els []map[string]any
	var kinds []string
	for _, k := range slices.Sorted(maps.Keys(elements)) {
		e := elements[k]
		if removed, _ := e.Properties["removed"].(bool); removed {
			continue
		}
		els = append(els, e.Properties)
		kinds = append(kinds, kindOfType(e.Type))
	}
	return decodeInto(props, els, kinds, def)
}

func ts(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// headerProps are the properties of a version's header node: the fields of the definition and the record's own.
func headerProps(def map[string]any, st Status, created, updated, published time.Time, by string) map[string]any {
	m := maps.Clone(def)
	m["status"] = string(st)
	for k, t := range map[string]time.Time{"createdAt": created, "updatedAt": updated, "publishedAt": published} {
		if v := ts(t); v != nil {
			m[k] = v
		}
	}
	if by != "" {
		m["updatedBy"] = by
	}
	return m
}

func jsonEqual(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// patch returns the update of stored properties to reach want (removed keys set to null), nil when they match.
func patch(have, want map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range want {
		if !jsonEqual(have[k], v) {
			out[k] = v
		}
	}
	for k, v := range have {
		if _, ok := want[k]; !ok && v != nil {
			out[k] = nil
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// versionEdits reconciles the nodes of a version with a definition: it creates or updates the header, creates, updates
// and, for the elements that left the definition, marks removed the element nodes, and ties new elements to the header.
func versionEdits(hkey, hType string, header map[string]any, els []defEl, old *domain.Node, oldChildren map[string]domain.Node) []graph.NodeEdit {
	var head graph.NodeEdit
	if old != nil {
		ref := old.Ref()
		head = graph.NodeEdit{Pre: &ref, Props: patch(old.Properties, header)}
	} else {
		head = graph.NodeEdit{Key: hkey, Type: hType, Props: header}
	}
	var edits []graph.NodeEdit
	want := map[string]bool{}
	for _, e := range els {
		k := hkey + "/" + e.kind + "/" + e.name
		want[k] = true
		nodeType := ""
		for _, dk := range defKinds {
			if dk.kind == e.kind {
				nodeType = dk.nodeType
			}
		}
		if n, ok := oldChildren[k]; ok {
			if p := patch(n.Properties, e.props); p != nil {
				edits = append(edits, update(n, p))
			}
			continue
		}
		edits = append(edits, graph.NodeEdit{Key: k, Type: nodeType, Props: e.props})
		head.Links = append(head.Links, graph.LinkEdit{Type: linkDefines, ToKey: k})
	}
	for _, k := range slices.Sorted(maps.Keys(oldChildren)) {
		if n := oldChildren[k]; !want[k] {
			if removed, _ := n.Properties["removed"].(bool); !removed {
				edits = append(edits, update(n, map[string]any{"removed": true}))
			}
		}
	}
	if head.Pre != nil && len(head.Props) == 0 && len(head.Links) == 0 {
		return edits
	}
	return append([]graph.NodeEdit{head}, edits...)
}

// read returns the versions at the head of main, looked at now.
func (s *GraphStore) read(ctx context.Context) (*defs, error) {
	d, _, err := s.cache.Fresh(ctx)
	if d == nil {
		return nil, err
	}
	return d, nil
}

// commit applies the items build makes from the current versions as one change on main; when main moved meanwhile, it
// reads the new head and builds them again (up to three retries).
func (s *GraphStore) commit(ctx context.Context, ns, title string, build func(*defs) ([]graph.NodeEdit, error)) error {
	for attempt := 0; ; attempt++ {
		d, err := s.read(ctx)
		if err != nil {
			return err
		}
		edits, err := build(d)
		if err != nil || len(edits) == 0 {
			return err
		}
		head, err := s.Graph.BranchHead(ctx, domain.MainBranch)
		if errors.Is(err, graph.ErrNotFound) {
			if head, err = s.Graph.CreateBaseline(ctx, "Repository", nil); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		_, err = s.Graph.Commit(ctx, graph.Commit{Namespace: ns, Title: title, Intent: title, Baseline: head.ID, By: "registrysvc", BaselineName: title, Edits: edits})
		if errors.Is(err, graph.ErrConflict) && attempt < 3 {
			continue // main moved: read again and rebuild
		}
		return err
	}
}

func update(n domain.Node, props map[string]any) graph.NodeEdit {
	ref := n.Ref()
	return graph.NodeEdit{Pre: &ref, Props: props}
}

// statusPatch is the update of a header node when a version changes status.
func statusPatch(st Status, at time.Time) map[string]any {
	p := map[string]any{"status": string(st), "updatedAt": ts(at)}
	if st == StatusPublished {
		p["publishedAt"] = ts(at)
	}
	return p
}

func markDeleted(n domain.Node) graph.NodeEdit {
	return update(n, map[string]any{"status": string(statusDeleted)})
}

// ---- methodologies ---------------------------------------------------------

func (s *GraphStore) Save(ctx context.Context, r Record) error {
	m := r.Methodology
	k := key(m.Name, m.Version)
	return s.commit(ctx, NamespaceMethodology, "Methodology "+k, func(d *defs) ([]graph.NodeEdit, error) {
		old, exists := d.methodologies[k]
		revive := exists && old.rec.Status == statusDeleted
		if exists && !revive && old.rec.Status != StatusDraft {
			return nil, fmt.Errorf("%s: %w", k, ErrImmutable)
		}
		created := r.UpdatedAt
		if exists && !revive {
			created = old.rec.CreatedAt
		}
		header, els, err := encodeMethodology(m)
		if err != nil {
			return nil, err
		}
		props := headerProps(header, r.Status, created, r.UpdatedAt, r.PublishedAt, r.UpdatedBy)
		var oldNode *domain.Node
		var oldChildren map[string]domain.Node
		if exists {
			n := old.node
			oldNode, oldChildren = &n, old.children
		}
		return versionEdits(MethodologyVersionKey(m.Name, m.Version), TypeMethodologyVersion, props, els, oldNode, oldChildren), nil
	})
}

func (s *GraphStore) Get(ctx context.Context, name, version string) (Record, error) {
	d, err := s.read(ctx)
	if err != nil {
		return Record{}, err
	}
	if version != "" {
		r, ok := d.methodologies[key(name, version)]
		if !ok || r.rec.Status == statusDeleted {
			return Record{}, fmt.Errorf("%s: %w", key(name, version), ErrNotFound)
		}
		return r.rec, nil
	}
	var best Record
	found := false
	for _, r := range d.methodologies {
		if r.rec.Methodology.Name == name && r.rec.Status == StatusPublished && (!found || r.rec.PublishedAt.After(best.PublishedAt)) {
			best, found = r.rec, true
		}
	}
	if !found {
		return Record{}, fmt.Errorf("%s (published): %w", name, ErrNotFound)
	}
	return best, nil
}

func (s *GraphStore) List(ctx context.Context) ([]Record, error) {
	d, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(d.methodologies))
	for _, r := range d.methodologies {
		if r.rec.Status != statusDeleted {
			out = append(out, r.rec)
		}
	}
	sortRecords(out)
	return out, nil
}

func (s *GraphStore) SetStatus(ctx context.Context, name, version string, st Status, at time.Time) error {
	k := key(name, version)
	return s.commit(ctx, NamespaceMethodology, fmt.Sprintf("Methodology %s %s", k, st), func(d *defs) ([]graph.NodeEdit, error) {
		old, ok := d.methodologies[k]
		if !ok || old.rec.Status == statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, ErrNotFound)
		}
		return []graph.NodeEdit{update(old.node, statusPatch(st, at))}, nil
	})
}

func (s *GraphStore) Delete(ctx context.Context, name, version string) error {
	k := key(name, version)
	return s.commit(ctx, NamespaceMethodology, "Delete methodology "+k, func(d *defs) ([]graph.NodeEdit, error) {
		old, ok := d.methodologies[k]
		if !ok || old.rec.Status == statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, ErrNotFound)
		}
		if old.rec.Status != StatusDraft {
			return nil, fmt.Errorf("%s: %w", k, ErrImmutable)
		}
		return []graph.NodeEdit{markDeleted(old.node)}, nil
	})
}
