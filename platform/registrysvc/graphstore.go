package registrysvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/lib/access"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/lib/graphsnap"
)

// TypeMethodologyVersion is the type of the header node of a methodology version (built-in meta-domain, ADR 0023): its
// scalar fields, status and timestamps; one node per element of the definition (see defs.go), tied by "defines" links.
const TypeMethodologyVersion = "methodology@MethodologyVersion"

// StoreGraph is what the store needs from the graph (*graph.Graph and the graph service client implement it).
type StoreGraph interface {
	BranchHeadHeader(ctx context.Context, namespace, name string) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
	// Commit runs a change of node edits (ADR 0024).
	Commit(ctx context.Context, in graphapi.Commit) (graphapi.CommitResult, error)
	// CreateChange and ImpactNodeCreate let a producer open a change on a namespace of its own and propose a pending (not
	// auto-accepted) created node in it (used for alias stubs, see aliasstubs.go); Changes lists every change, for
	// the pending-stub dedup check.
	CreateChange(ctx context.Context, in graphapi.NewChange) (domain.Change, error)
	ImpactNodeCreate(ctx context.Context, id domain.ChangeID, in graphapi.NodeCreate) (domain.ChangeImpact, error)
	Changes(ctx context.Context) ([]domain.Change, error)
	// Structures give the root project, where the registry's own changes act (ADR 0091).
	Structures(ctx context.Context) (domain.Structures, error)
}

// rootProject is the project the registry's own changes (methodology and domain seeds, alias stubs) act in.
func rootProject(ctx context.Context, g StoreGraph) (string, error) {
	st, err := g.Structures(ctx)
	if err != nil {
		return "", err
	}
	return st.Project().Root, nil
}

// linkDefines is the link type that ties a methodology version to the elements of its definition.
const linkDefines = "methodology@defines"

// statusDeleted marks a deleted draft: a node key is never freed on a versioned graph, so the node stays and a later
// Save of the same version revives it. The stores treat it as absent.
const statusDeleted Status = "deleted"

// NamespaceMethodology is the namespace of the methodology versions and their elements. The registry API is what callers
// use; the graph holds the content.
const NamespaceMethodology = domain.NamespaceMethodology

// MethodologyVersionKey is the key of the node of a methodology version.
func MethodologyVersionKey(name, version string) string { return "MV:" + key(name, version) }

// GraphStore keeps methodology versions as nodes of the graph, changed through changes applied on main: they are
// versioned, journaled and reviewable like any node (each element has its own history). It implements Store; the domains
// are in a DomainStore of the registry (ADR 0023).
type GraphStore struct {
	Graph StoreGraph

	view *graphsnap.View[*defs]
	// follow keeps the head the methodologies were read at until a commit of them moves it (Follow); nil: the head is
	// asked at each read
	follow *graphsnap.Heads
}

var _ Store = (*GraphStore)(nil)

// aliasStubGraph exposes the underlying graph to ensureAliasStubs (aliasstubs.go): satisfied by *GraphStore
// and by anything embedding it (e.g. a Store that also holds domains), through Go's method promotion.
func (s *GraphStore) aliasStubGraph() StoreGraph { return s.Graph }

// NewGraphStore returns a store over a graph (the graph itself, or a client of the graph service).
func NewGraphStore(g StoreGraph) *GraphStore {
	s := &GraphStore{Graph: g}
	s.view = graphsnap.One(graphsnap.NewReader(g), NamespaceMethodology, true, buildDefs)
	return s
}

// ShareReader makes the store read the methodologies through the reader of the graph of the process (ADR 0145),
// before its first read.
func (s *GraphStore) ShareReader(r *graphsnap.Reader) *GraphStore {
	s.view = graphsnap.One(r, NamespaceMethodology, true, buildDefs)
	s.view.Heads = s.follow
	return s
}

// Follow makes the store keep the methodologies it read, compiled ones included, until a commit of them moves the head
// of their namespace (ADR 0168): Moved, called on the graph's baseline events (FollowEvents) and after the store's own
// commits. A graph asking the registry about a change while it holds the transaction of that change (the guardian of
// a commit) is then answered without being read again.
func (s *GraphStore) Follow() *GraphStore {
	s.follow = &graphsnap.Heads{MaxAge: access.FollowAge}
	s.view.Heads = s.follow
	return s
}

// FollowEvents follows the graph's baseline events on bus (Follow).
func (s *GraphStore) FollowEvents(bus access.Subscriber) error {
	s.Follow()
	return bus.Subscribe(fmt.Sprintf(domain.SubjectBaselineAdvanced, domain.MainBranch), s.Moved)
}

// Moved takes a baseline event of the graph (a domain.BaselineEvent): a commit on the main branch of the namespace of
// the methodologies makes the store read them again at its next use. Other events are left alone.
func (s *GraphStore) Moved(data []byte) {
	if s.follow == nil {
		return
	}
	var ev domain.BaselineEvent
	if json.Unmarshal(data, &ev) != nil || (ev.Branch == domain.MainBranch && (ev.Namespace == "" || ev.Namespace == NamespaceMethodology)) {
		s.follow.Moved()
	}
}

func (s *GraphStore) moved() {
	if s.follow != nil {
		s.follow.Moved()
	}
}

// withSuspects reads a baseline with its suspect links too: a link to an element is a link to the version the element
// had when the reference was written (ADR 0003), and editing the element (a new version of its node) leaves the
// references to it in place, suspect. They are references all the same (ADR 0120).
type withSuspects struct{ StoreGraph }

func (w withSuspects) BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error) {
	return graphsnap.BaselineWithSuspects(ctx, w.StoreGraph, id)
}

// defs is the versions of a baseline, decoded once per head of main.
type defs struct {
	methodologies map[string]stored[Record]
	problems      []string
	// parentOf is the activity each activity is a part of, by key, following the sub_activity links, suspect ones
	// included (a step edited since keeps its parent)
	parentOf map[string]string
}

// stored is a decoded version with the nodes it comes from: the header and every element node (removed ones too, so that
// an element deleted from a draft and added again is revived, not created twice), and the outgoing links of each, with
// the key of their target.
type stored[T any] struct {
	node     domain.Node
	rec      T
	children map[string]domain.Node // by key
	out      map[domain.NodeID][]keyedLink
}

// keyedLink is a link with the key of its target.
type keyedLink struct {
	domain.Link
	toKey string
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

func buildDefs(_ domain.BaselineID, nodes []domain.Node, links []domain.Link) *defs {
	d := &defs{methodologies: map[string]stored[Record]{}, parentOf: parentsOf(nodes, links)}
	type owner struct{ ns, key string }
	children := map[owner]map[string]domain.Node{} // header -> element nodes
	byID := map[domain.NodeID]domain.Node{}
	for _, n := range nodes {
		if n.Namespace != NamespaceMethodology {
			continue
		}
		byID[n.ID] = n
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
	out := map[domain.NodeID][]keyedLink{}
	for _, l := range links {
		from, ok := byID[l.From.ID]
		to, ok2 := byID[l.To.ID]
		if !ok || !ok2 || from.Version != l.From.Version {
			continue
		}
		out[l.From.ID] = append(out[l.From.ID], keyedLink{l, to.Key})
	}
	for _, ls := range out {
		sort.SliceStable(ls, func(i, j int) bool { return ls[i].ID < ls[j].ID })
	}
	for _, n := range nodes {
		if n.Namespace != NamespaceMethodology || n.Type != TypeMethodologyVersion {
			continue
		}
		var r Record
		els := children[owner{n.Namespace, n.Key}]
		if err := decodeVersion(n, els, out, byID, &r.Methodology, &r.Status, &r.CreatedAt, &r.UpdatedAt, &r.PublishedAt, &r.UpdatedBy); err != nil {
			d.problems = append(d.problems, fmt.Sprintf("%s: %v", n.Key, err))
			continue
		}
		mine := map[domain.NodeID][]keyedLink{n.ID: out[n.ID]}
		for _, e := range els {
			mine[e.ID] = out[e.ID]
		}
		d.methodologies[key(r.Methodology.Name, r.Methodology.Version)] = stored[Record]{n, r, els, mine}
	}
	return d
}

// decodeVersion assembles a definition from its header node, its live element nodes and their links: the references
// back in the properties (methodology.DecodeRefs), the steps of a process or a method from its sub_activity links.
func decodeVersion(n domain.Node, elements map[string]domain.Node, out map[domain.NodeID][]keyedLink, byID map[domain.NodeID]domain.Node, def any, status *Status, created, updated, published *time.Time, by *string) error {
	raw, err := versionJSON(n, elements, out, byID, status, created, updated, published, by)
	if err != nil {
		return err
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, def); err != nil {
		return fmt.Errorf("definition: %w", err)
	}
	return nil
}

// versionJSON is the JSON of the definition decodeVersion assembles, before it is read into a methodology.
func versionJSON(n domain.Node, elements map[string]domain.Node, out map[domain.NodeID][]keyedLink, byID map[domain.NodeID]domain.Node, status *Status, created, updated, published *time.Time, by *string) (map[string]any, error) {
	props := maps.Clone(n.Properties)
	s, _ := props["status"].(string)
	*status = Status(s)
	*by, _ = props["updatedBy"].(string)
	for k, dst := range map[string]*time.Time{"createdAt": created, "updatedAt": updated, "publishedAt": published} {
		if s, _ := props[k].(string); s != "" {
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			*dst = t
		}
	}
	for _, k := range metaKeys {
		delete(props, k)
	}
	refs := func(id domain.NodeID) []methodology.RefTarget {
		var ls []methodology.RefTarget
		for _, l := range out[id] {
			to := byID[l.To.ID]
			name, _ := to.Properties["name"].(string)
			ls = append(ls, methodology.RefTarget{Type: l.Type, Props: l.Properties, Kind: kindOfType(to.Type), Name: name})
		}
		return ls
	}
	props = methodology.DecodeRefs(methodology.ElementVersion, props, refs(n.ID))
	var stepsOf func(id domain.NodeID) []any
	stepsOf = func(id domain.NodeID) []any {
		var kids []domain.Node
		for _, l := range out[id] {
			if l.Type != linkSubActivity {
				continue
			}
			if c, ok := byID[l.To.ID]; ok {
				if removed, _ := c.Properties["removed"].(bool); !removed {
					kids = append(kids, c)
				}
			}
		}
		sort.SliceStable(kids, func(i, j int) bool {
			a, _ := kids[i].Properties["position"].(float64)
			b, _ := kids[j].Properties["position"].(float64)
			return a < b
		})
		var steps []any
		for _, c := range kids {
			p := stepJSON(methodology.DecodeRefs(kindOfType(c.Type), c.Properties, refs(c.ID)))
			if sub := stepsOf(c.ID); len(sub) > 0 {
				p["steps"] = sub
			}
			steps = append(steps, p)
		}
		return steps
	}
	var els []map[string]any
	var kinds []string
	for _, k := range slices.Sorted(maps.Keys(elements)) {
		e := elements[k]
		kind := kindOfType(e.Type)
		if removed, _ := e.Properties["removed"].(bool); removed || kind == kindStep || kind == kindMethodStep {
			continue // a step is read through its process or method
		}
		p := methodology.DecodeRefs(kind, e.Properties, refs(e.ID))
		if kind == kindProcess || kind == kindMethod {
			if steps := stepsOf(e.ID); len(steps) > 0 {
				p["steps"] = steps
			}
		}
		els = append(els, p)
		kinds = append(kinds, kind)
	}
	return join(props, els, kinds), nil
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

// versionEdits reconciles the nodes of a version with a definition (an elementGraph): it creates or updates the
// header, creates, updates and, for the elements that left the definition, marks removed the element nodes (their
// links dropped), ties new elements to the header (defines) and brings the links of every node to the references of
// the definition (ADR 0120): a link the definition no longer makes is removed, one it makes is added, one whose
// attributes changed is replaced. old, oldChildren and oldOut are the stored version, nil for a new one.
func versionEdits(hkey, hType string, eg elementGraph, old *domain.Node, oldChildren map[string]domain.Node, oldOut map[domain.NodeID][]keyedLink) []graphapi.NodeEdit {
	// linkEdits diffs the links of a stored node (only the types managed counts) with the wanted ones
	linkEdits := func(id domain.NodeID, want []methodology.RefLink, managed func(string) bool) (add []graphapi.LinkEdit, remove []domain.LinkID) {
		used := make([]bool, len(want))
		for _, l := range oldOut[id] {
			if !managed(l.Type) {
				continue
			}
			to := strings.TrimPrefix(l.toKey, hkey+"/")
			kept := false
			for i, w := range want {
				if !used[i] && sameRef(w, l.Type, to, l.Properties) {
					used[i], kept = true, true
					break
				}
			}
			if !kept {
				remove = append(remove, l.ID)
			}
		}
		for i, w := range want {
			if !used[i] {
				add = append(add, graphapi.LinkEdit{Type: w.Type, ToKey: hkey + "/" + w.To, Props: normProps(w.Props)})
			}
		}
		return add, remove
	}
	all := func(string) bool { return true }
	var head graphapi.NodeEdit
	if old != nil {
		ref := old.Ref()
		head = graphapi.NodeEdit{Pre: &ref, Props: patch(old.Properties, eg.header)}
		head.Links, head.RemoveLinks = linkEdits(old.ID, eg.headerLinks, func(t string) bool { return t != linkDefines })
	} else {
		head = graphapi.NodeEdit{Key: hkey, Type: hType, Props: eg.header}
		head.Links, _ = linkEdits("", eg.headerLinks, all)
	}
	var edits []graphapi.NodeEdit
	want := map[string]bool{}
	for _, e := range eg.els {
		rel := e.kind + "/" + e.name
		k := hkey + "/" + rel
		want[k] = true
		if n, ok := oldChildren[k]; ok {
			p := patch(n.Properties, e.props)
			add, remove := linkEdits(n.ID, eg.links[rel], all)
			if p != nil || len(add) > 0 || len(remove) > 0 {
				ref := n.Ref()
				edits = append(edits, graphapi.NodeEdit{Pre: &ref, Props: p, Links: add, RemoveLinks: remove})
			}
			continue
		}
		add, _ := linkEdits("", eg.links[rel], all)
		edits = append(edits, graphapi.NodeEdit{Key: k, Type: nodeTypeOf(e.kind), Props: e.props, Links: add})
		head.Links = append(head.Links, graphapi.LinkEdit{Type: linkDefines, ToKey: k})
	}
	for _, k := range slices.Sorted(maps.Keys(oldChildren)) {
		if n := oldChildren[k]; !want[k] {
			if removed, _ := n.Properties["removed"].(bool); !removed {
				ed := update(n, map[string]any{"removed": true})
				_, ed.RemoveLinks = linkEdits(n.ID, nil, all)
				edits = append(edits, ed)
			}
		}
	}
	if head.Pre != nil && len(head.Props) == 0 && len(head.Links) == 0 && len(head.RemoveLinks) == 0 {
		return edits
	}
	return append([]graphapi.NodeEdit{head}, edits...)
}

// read returns the versions at the head of main, looked at now.
func (s *GraphStore) read(ctx context.Context) (*defs, error) {
	d, _, err := s.view.Get(ctx)
	if d == nil {
		return nil, err
	}
	return d, nil
}

// commit applies the items build makes from the current versions as one change on main; when main moved meanwhile, it
// reads the new head and builds them again (up to three retries).
func (s *GraphStore) carriesEvents() {}

// commit commits the edits build gives, with the registry's events about them (ADR 0136). The graph takes events from a
// platform service only: a commit carrying some goes as the registry, for the person it acts for.
func (s *GraphStore) commit(ctx context.Context, ns, title string, build func(*defs) ([]graphapi.NodeEdit, error), events ...graphapi.CarriedEvent) error {
	if len(events) > 0 && !authz.From(ctx).System() {
		ctx = authz.With(ctx, authz.System("registry", access.RoleAdmin).For(authz.From(ctx).EndUser()))
	}
	for attempt := 0; ; attempt++ {
		d, err := s.read(ctx)
		if err != nil {
			return err
		}
		edits, err := build(d)
		if err != nil || len(edits) == 0 {
			return err
		}
		head, err := s.Graph.BranchHeadHeader(ctx, ns, domain.MainBranch)
		if err != nil {
			return err
		}
		root, rerr := rootProject(ctx, s.Graph)
		if rerr != nil {
			return rerr
		}
		_, err = s.Graph.Commit(ctx, graphapi.Commit{Namespace: ns, Title: title, Intent: title, Baseline: head.ID, By: "registrysvc", BaselineName: title, Edits: edits, ProjectID: root, Events: events})
		s.moved() // its own commit: read again, whatever the event says later
		if errors.Is(err, graphapi.ErrConflict) && attempt < 3 {
			continue // main moved: read again and rebuild
		}
		if errors.Is(err, graphapi.ErrNothingToCommit) {
			return nil // the graph holds the definition already
		}
		return err
	}
}

func update(n domain.Node, props map[string]any) graphapi.NodeEdit {
	ref := n.Ref()
	return graphapi.NodeEdit{Pre: &ref, Props: props}
}

// statusPatch is the update of a header node when a version changes status.
func statusPatch(st Status, at time.Time) map[string]any {
	p := map[string]any{"status": string(st), "updatedAt": ts(at)}
	if st == StatusPublished {
		p["publishedAt"] = ts(at)
	}
	return p
}

func markDeleted(n domain.Node) graphapi.NodeEdit {
	return update(n, map[string]any{"status": string(statusDeleted)})
}

// ---- methodologies ---------------------------------------------------------

// encodeVersion is a definition as the graph holds it: the header, an element per declared element and per step, and
// the links of their references (ADR 0120).
func encodeVersion(m methodology.Methodology, st Status, created, updated, published time.Time, by string) (elementGraph, error) {
	header, els, err := encodeMethodology(m)
	if err != nil {
		return elementGraph{}, err
	}
	stepEls, subActivity := stepChildren(els)
	els = append(els, stepEls...)
	return buildElementGraph(headerProps(header, st, created, updated, published, by), els, subActivity), nil
}

func (s *GraphStore) Save(ctx context.Context, r Record, events ...graphapi.CarriedEvent) error {
	m := r.Methodology
	k := key(m.Name, m.Version)
	return s.commit(ctx, NamespaceMethodology, "Methodology "+k, func(d *defs) ([]graphapi.NodeEdit, error) {
		old, exists := d.methodologies[k]
		revive := exists && old.rec.Status == statusDeleted
		if exists && !revive && old.rec.Status != StatusDraft {
			return nil, fmt.Errorf("%s: %w", k, ErrImmutable)
		}
		created := r.UpdatedAt
		if exists && !revive {
			created = old.rec.CreatedAt
		}
		eg, err := encodeVersion(m, r.Status, created, r.UpdatedAt, r.PublishedAt, r.UpdatedBy)
		if err != nil {
			return nil, err
		}
		var oldNode *domain.Node
		var oldChildren map[string]domain.Node
		var oldOut map[domain.NodeID][]keyedLink
		if exists {
			n := old.node
			oldNode, oldChildren, oldOut = &n, old.children, old.out
		}
		return versionEdits(MethodologyVersionKey(m.Name, m.Version), TypeMethodologyVersion, eg, oldNode, oldChildren, oldOut), nil
	}, events...)
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

func (s *GraphStore) SetStatus(ctx context.Context, name, version string, st Status, at time.Time, events ...graphapi.CarriedEvent) error {
	k := key(name, version)
	return s.commit(ctx, NamespaceMethodology, fmt.Sprintf("Methodology %s %s", k, st), func(d *defs) ([]graphapi.NodeEdit, error) {
		old, ok := d.methodologies[k]
		if !ok || old.rec.Status == statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, ErrNotFound)
		}
		return []graphapi.NodeEdit{update(old.node, statusPatch(st, at))}, nil
	}, events...)
}

func (s *GraphStore) Delete(ctx context.Context, name, version string, events ...graphapi.CarriedEvent) error {
	k := key(name, version)
	return s.commit(ctx, NamespaceMethodology, "Delete methodology "+k, func(d *defs) ([]graphapi.NodeEdit, error) {
		old, ok := d.methodologies[k]
		if !ok || old.rec.Status == statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, ErrNotFound)
		}
		if old.rec.Status != StatusDraft {
			return nil, fmt.Errorf("%s: %w", k, ErrImmutable)
		}
		return []graphapi.NodeEdit{markDeleted(old.node)}, nil
	}, events...)
}
