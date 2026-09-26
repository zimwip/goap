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

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/graphsnap"
	"github.com/zimwip/goap/pkg/metamodel"
)

// Namespaces of the stored definitions: methodology versions and their elements live in the "methodology" namespace, domain
// versions and theirs in the "domain" namespace.
const (
	NamespaceMethodology = "methodology"
	NamespaceDomain      = "domain"
)

// Node types of the stored definitions: one header node per version of a methodology or of a domain (scalar fields, timestamps;
// its status is the node's lifecycle state) and one node per element of the definition (see defs.go), tied by "defines" links. The
// definition is what these nodes hold; the elements a published methodology projects at run time (metamodel, keys M:/D:) are
// derived from it. The node types and the `version` lifecycle come from the platform domain, seeded before the registry writes.
const (
	TypeMethodologyVersion = "MethodologyVersion"
	TypeDomainVersion      = "DomainVersion"
	// LinkDefines ties a version to the elements of its definition.
	LinkDefines = "defines"
)

// States of the `version` lifecycle. draft is the persisted state of a version being written: it is reopened (editing) inside a change
// to be edited and closed again, which is how the graph keeps a published version frozen.
const (
	stateDraft     = "draft"
	stateEditing   = "editing"
	statePublished = "published"
	stateArchived  = "archived"
	stateDeleted   = "deleted"
)

// statusDeleted is the internal status of a discarded draft: a node key is never freed on a versioned graph, so the node stays
// (state deleted) and a later Save of the same version restores it. The stores treat it as absent.
const statusDeleted Status = "deleted"

// ErrMetadataMissing is returned when the graph has not been seeded with the platform domain yet (node types and lifecycle of the
// stored versions): the caller retries.
var ErrMetadataMissing = errors.New("the graph metadata is not seeded yet")

// MethodologyVersionKey is the key of the node of a methodology version.
func MethodologyVersionKey(name, version string) string { return "MV:" + key(name, version) }

// DomainVersionKey is the key of the node of a domain version.
func DomainVersionKey(name, version string) string { return "DV:" + key(name, version) }

// GraphStore keeps methodology and domain versions as nodes of the graph, changed through changes applied on main: they
// are versioned, journaled and reviewable like any node (each element has its own history), and the registry needs no database
// of its own. It implements Store and DomainStore.
type GraphStore struct {
	Graph metamodel.Graph
	Now   func() time.Time

	cache graphsnap.Cache[*defs]
}

var (
	_ Store       = (*GraphStore)(nil)
	_ DomainStore = (*GraphStore)(nil)
)

// NewGraphStore returns a store over a graph (the graph itself, or a client of the graph service).
func NewGraphStore(g metamodel.Graph) *GraphStore {
	s := &GraphStore{Graph: g, Now: time.Now}
	s.cache = graphsnap.Cache[*defs]{Graph: g, Build: buildDefs}
	return s
}

// defs is the versions of a baseline, decoded once per head of main.
type defs struct {
	methodologies map[string]stored[Record]
	domains       map[string]stored[DomainRecord]
	problems      []string
	// ready: the node types of the versions are on the graph, so that their nodes get their lifecycle
	ready bool
}

// stored is a decoded version with the nodes it comes from: the header and every element node (removed ones too, so that
// an element deleted from a draft and added again is revived, not created twice).
type stored[T any] struct {
	node     domain.Node
	rec      T
	children map[string]domain.Node // by key
}

var metaKeys = []string{"createdAt", "updatedAt", "publishedAt", "updatedBy"}

func kindOfType(t string) string {
	for _, k := range defKinds {
		if k.nodeType == t {
			return k.kind
		}
	}
	return ""
}

func statusOf(state string) Status {
	switch state {
	case statePublished:
		return StatusPublished
	case stateArchived:
		return StatusArchived
	case stateDeleted:
		return statusDeleted
	}
	return StatusDraft
}

func buildDefs(_ domain.BaselineID, nodes []domain.Node, _ []domain.Link) *defs {
	d := &defs{methodologies: map[string]stored[Record]{}, domains: map[string]stored[DomainRecord]{}}
	type owner struct{ ns, key string }
	children := map[owner]map[string]domain.Node{} // header -> element nodes
	metaKey := metamodel.DomainKey(domain.NamespacePlatform, TypeMethodologyVersion)
	for _, n := range nodes {
		if n.Type == metamodel.TypeNodeType && n.Key == metaKey {
			_, d.ready = n.Properties["lifecycle"]
		}
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
		switch {
		case n.Namespace == NamespaceMethodology && n.Type == TypeMethodologyVersion:
			var r Record
			if err := decodeVersion(n, children[owner{n.Namespace, n.Key}], true, &r.Methodology, &r.Status, &r.CreatedAt, &r.UpdatedAt, &r.PublishedAt, &r.UpdatedBy); err != nil {
				d.problems = append(d.problems, fmt.Sprintf("%s: %v", n.Key, err))
				continue
			}
			d.methodologies[key(r.Methodology.Name, r.Methodology.Version)] = stored[Record]{n, r, children[owner{n.Namespace, n.Key}]}
		case n.Namespace == NamespaceDomain && n.Type == TypeDomainVersion:
			var r DomainRecord
			if err := decodeVersion(n, children[owner{n.Namespace, n.Key}], false, &r.Domain, &r.Status, &r.CreatedAt, &r.UpdatedAt, &r.PublishedAt, &r.UpdatedBy); err != nil {
				d.problems = append(d.problems, fmt.Sprintf("%s: %v", n.Key, err))
				continue
			}
			d.domains[key(r.Domain.Name, r.Domain.Version)] = stored[DomainRecord]{n, r, children[owner{n.Namespace, n.Key}]}
		}
	}
	return d
}

// decodeVersion assembles a definition from its header node and its live element nodes.
func decodeVersion(n domain.Node, elements map[string]domain.Node, isMethodology bool, def any, status *Status, created, updated, published *time.Time, by *string) error {
	props := maps.Clone(n.Properties)
	*status = statusOf(n.State)
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
	return decodeInto(props, els, kinds, isMethodology, def)
}

func ts(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// headerProps are the properties of a version's header node: the fields of the definition and the record's own.
func headerProps(def map[string]any, created, updated, published time.Time, by string) map[string]any {
	m := maps.Clone(def)
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

func withoutMeta(p map[string]any) map[string]any {
	out := maps.Clone(p)
	for _, k := range metaKeys {
		delete(out, k)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func create(typ, k string, props map[string]any) domain.ChangeItem {
	return domain.ChangeItem{ID: domain.ItemID(uuid.NewString()), Kind: domain.KindProposal, Type: "registry", ProducedBy: "registrysvc",
		Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: k, Type: typ, Properties: props}}}
}

func update(n domain.Node, props map[string]any) domain.ChangeItem {
	ref := n.Ref()
	return domain.ChangeItem{Kind: domain.KindProposal, Type: "registry", ProducedBy: "registrysvc",
		Proposal: &domain.Proposal{Op: domain.OpUpdateNode, Node: &domain.NodeDraft{Base: &ref, Properties: props}}}
}

func move(n domain.Node, to string) domain.ChangeItem {
	ref := n.Ref()
	return domain.ChangeItem{Kind: domain.KindProposal, Type: "registry", ProducedBy: "registrysvc",
		Proposal: &domain.Proposal{Op: domain.OpTransitionNode, Node: &domain.NodeDraft{Base: &ref, State: to}}}
}

// edit changes the properties of a node of a persisted version: it is reopened, changed and closed again. A node with no state
// (stored before lifecycles) is edited directly.
func edit(n domain.Node, props map[string]any) []domain.ChangeItem {
	if n.State != stateDraft {
		return []domain.ChangeItem{update(n, props)}
	}
	return []domain.ChangeItem{move(n, stateEditing), update(n, props), move(n, stateDraft)}
}

// versionItems reconciles the nodes of a version with a definition. Nothing is proposed when its content did not change (the
// timestamps alone do not count). A discarded version is restored first. It creates the header and the elements of a new version,
// edits those that changed, marks removed the elements that left the definition and ties new elements to the header.
func versionItems(hkey, hType string, header map[string]any, els []defEl, old *domain.Node, oldChildren map[string]domain.Node) []domain.ChangeItem {
	var items []domain.ChangeItem
	var hEnd domain.Endpoint
	if old == nil {
		it := create(hType, hkey, header)
		hEnd = domain.Endpoint{Item: it.ID}
		items = append(items, it)
	} else {
		ref := old.Ref()
		hEnd = domain.Endpoint{Node: &ref}
	}
	var elItems []domain.ChangeItem
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
				elItems = append(elItems, edit(n, p)...)
			}
			continue
		}
		it := create(nodeType, k, e.props)
		elItems = append(elItems, it, domain.ChangeItem{Kind: domain.KindProposal, Type: "registry", ProducedBy: "registrysvc",
			Proposal: &domain.Proposal{Op: domain.OpAddLink, Link: &domain.LinkDraft{Type: LinkDefines, From: hEnd, To: domain.Endpoint{Item: it.ID}}}})
	}
	for _, k := range slices.Sorted(maps.Keys(oldChildren)) {
		if n := oldChildren[k]; !want[k] {
			if removed, _ := n.Properties["removed"].(bool); !removed {
				elItems = append(elItems, edit(n, map[string]any{"removed": true})...)
			}
		}
	}
	if old == nil {
		return append(items, elItems...)
	}
	restore := old.State == stateDeleted
	hp := patch(old.Properties, header)
	if !restore && withoutMeta(hp) == nil && len(elItems) == 0 {
		return nil // the content did not change
	}
	if restore {
		items = append(items, move(*old, stateDraft))
		cur := *old
		cur.State = stateDraft
		old = &cur
	}
	if hp != nil {
		items = append(items, edit(*old, hp)...)
	}
	return append(items, elItems...)
}

// publishItems publishes a draft: the header takes its timestamps and moves on from `editing`, the live elements from `draft`.
func publishItems(header domain.Node, children map[string]domain.Node, at time.Time) []domain.ChangeItem {
	items := []domain.ChangeItem{move(header, stateEditing), update(header, map[string]any{"publishedAt": ts(at), "updatedAt": ts(at)}), move(header, statePublished)}
	for _, k := range slices.Sorted(maps.Keys(children)) {
		if n := children[k]; !isRemoved(n) && n.State == stateDraft {
			items = append(items, move(n, statePublished))
		}
	}
	return items
}

func archiveItems(header domain.Node, children map[string]domain.Node) []domain.ChangeItem {
	items := []domain.ChangeItem{move(header, stateArchived)}
	for _, k := range slices.Sorted(maps.Keys(children)) {
		if n := children[k]; !isRemoved(n) && n.State == statePublished {
			items = append(items, move(n, stateArchived))
		}
	}
	return items
}

func isRemoved(n domain.Node) bool { removed, _ := n.Properties["removed"].(bool); return removed }

// read returns the versions at the head of main, looked at now.
func (s *GraphStore) read(ctx context.Context) (*defs, error) {
	d, _, err := s.cache.Fresh(ctx)
	if d == nil {
		return nil, err
	}
	return d, nil
}

// commit applies the items build makes from the current versions as one change of a namespace on main, once more on the new
// head when main moved meanwhile.
func (s *GraphStore) commit(ctx context.Context, ns, title string, build func(*defs) ([]domain.ChangeItem, error)) error {
	for attempt := 0; ; attempt++ {
		d, err := s.read(ctx)
		if err != nil {
			return err
		}
		if !d.ready {
			return ErrMetadataMissing
		}
		items, err := build(d)
		if err != nil || len(items) == 0 {
			return err
		}
		head, err := s.Graph.BranchHead(ctx, domain.MainBranch)
		if err != nil {
			return err
		}
		c, err := s.Graph.CreateChange(ctx, graph.NewChange{Namespace: ns, Title: title, Intent: title, BaselineID: head.ID})
		if err != nil {
			return err
		}
		if _, err = s.Graph.AddItems(ctx, c.ID, items); err == nil {
			_, err = s.Graph.Apply(ctx, c.ID, title)
		}
		if errors.Is(err, graph.ErrConflict) && attempt < 3 {
			continue // main moved: read again and rebuild
		}
		return err
	}
}

func (s *GraphStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// setStatus moves a stored version on. A version that is not where the move starts is refused (published versions are frozen).
func setStatus[T any](st stored[T], name string, to Status, at time.Time) ([]domain.ChangeItem, error) {
	if st.node.State == "" { // stored before lifecycles
		return nil, fmt.Errorf("%s: %w", name, ErrImmutable)
	}
	switch to {
	case StatusPublished:
		switch st.node.State {
		case stateDraft:
			return publishItems(st.node, st.children, at), nil
		case statePublished:
			return nil, nil
		}
	case StatusArchived:
		switch st.node.State {
		case statePublished:
			return archiveItems(st.node, st.children), nil
		case stateArchived:
			return nil, nil
		}
	case statusDeleted:
		if st.node.State == stateDraft {
			return []domain.ChangeItem{move(st.node, stateDeleted)}, nil
		}
	}
	return nil, fmt.Errorf("%s is %s, it cannot become %s: %w", name, statusOf(st.node.State), to, ErrImmutable)
}

// ---- methodologies ---------------------------------------------------------

func (s *GraphStore) Save(ctx context.Context, r Record) error {
	m := r.Methodology
	k := key(m.Name, m.Version)
	return s.commit(ctx, NamespaceMethodology, "Methodology "+k, func(d *defs) ([]domain.ChangeItem, error) {
		old, exists := d.methodologies[k]
		if exists && old.rec.Status != StatusDraft && old.rec.Status != statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, ErrImmutable)
		}
		created := r.UpdatedAt
		if exists && old.rec.Status == StatusDraft {
			created = old.rec.CreatedAt
		}
		header, els, err := encodeMethodology(m)
		if err != nil {
			return nil, err
		}
		props := headerProps(header, created, r.UpdatedAt, r.PublishedAt, r.UpdatedBy)
		var oldNode *domain.Node
		var oldChildren map[string]domain.Node
		if exists {
			n := old.node
			oldNode, oldChildren = &n, old.children
		}
		return versionItems(MethodologyVersionKey(m.Name, m.Version), TypeMethodologyVersion, props, els, oldNode, oldChildren), nil
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
	return s.commit(ctx, NamespaceMethodology, fmt.Sprintf("Methodology %s %s", k, st), func(d *defs) ([]domain.ChangeItem, error) {
		old, ok := d.methodologies[k]
		if !ok || old.rec.Status == statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, ErrNotFound)
		}
		return setStatus(old, k, st, at)
	})
}

func (s *GraphStore) Delete(ctx context.Context, name, version string) error {
	k := key(name, version)
	return s.commit(ctx, NamespaceMethodology, "Delete methodology "+k, func(d *defs) ([]domain.ChangeItem, error) {
		old, ok := d.methodologies[k]
		if !ok || old.rec.Status == statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, ErrNotFound)
		}
		return setStatus(old, k, statusDeleted, s.now())
	})
}

// ---- domains -----------------------------------------------------------------

func (s *GraphStore) SaveDomain(ctx context.Context, r DomainRecord) error {
	dm := r.Domain
	k := key(dm.Name, dm.Version)
	return s.commit(ctx, NamespaceDomain, "Domain "+k, func(d *defs) ([]domain.ChangeItem, error) {
		old, exists := d.domains[k]
		if exists && old.rec.Status != StatusDraft && old.rec.Status != statusDeleted {
			return nil, fmt.Errorf("domain %s: %w", k, ErrImmutable)
		}
		created := r.UpdatedAt
		if exists && old.rec.Status == StatusDraft {
			created = old.rec.CreatedAt
		}
		header, els, err := encodeDomain(dm)
		if err != nil {
			return nil, err
		}
		props := headerProps(header, created, r.UpdatedAt, r.PublishedAt, r.UpdatedBy)
		var oldNode *domain.Node
		var oldChildren map[string]domain.Node
		if exists {
			n := old.node
			oldNode, oldChildren = &n, old.children
		}
		return versionItems(DomainVersionKey(dm.Name, dm.Version), TypeDomainVersion, props, els, oldNode, oldChildren), nil
	})
}

func (s *GraphStore) GetDomain(ctx context.Context, name, version string) (DomainRecord, error) {
	d, err := s.read(ctx)
	if err != nil {
		return DomainRecord{}, err
	}
	if version != "" {
		r, ok := d.domains[key(name, version)]
		if !ok || r.rec.Status == statusDeleted {
			return DomainRecord{}, fmt.Errorf("%s: %w", key(name, version), errDomainNotFound)
		}
		return r.rec, nil
	}
	var best DomainRecord
	found := false
	for _, r := range d.domains {
		if r.rec.Domain.Name == name && r.rec.Status == StatusPublished && (!found || r.rec.PublishedAt.After(best.PublishedAt)) {
			best, found = r.rec, true
		}
	}
	if !found {
		return DomainRecord{}, fmt.Errorf("%s (published): %w", name, errDomainNotFound)
	}
	return best, nil
}

func (s *GraphStore) ListDomains(ctx context.Context) ([]DomainRecord, error) {
	d, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DomainRecord, 0, len(d.domains))
	for _, r := range d.domains {
		if r.rec.Status != statusDeleted {
			out = append(out, r.rec)
		}
	}
	sortDomainRecords(out)
	return out, nil
}

func (s *GraphStore) SetDomainStatus(ctx context.Context, name, version string, st Status, at time.Time) error {
	k := key(name, version)
	return s.commit(ctx, NamespaceDomain, fmt.Sprintf("Domain %s %s", k, st), func(d *defs) ([]domain.ChangeItem, error) {
		old, ok := d.domains[k]
		if !ok || old.rec.Status == statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, errDomainNotFound)
		}
		return setStatus(old, "domain "+k, st, at)
	})
}

func (s *GraphStore) DeleteDomain(ctx context.Context, name, version string) error {
	k := key(name, version)
	return s.commit(ctx, NamespaceDomain, "Delete domain "+k, func(d *defs) ([]domain.ChangeItem, error) {
		old, ok := d.domains[k]
		if !ok || old.rec.Status == statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, errDomainNotFound)
		}
		return setStatus(old, "domain "+k, statusDeleted, s.now())
	})
}
