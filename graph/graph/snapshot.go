package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/contracts/domain"
)

// This file is the export and import of the graph of a namespace as a snapshot (ADR 0104): the state a baseline
// holds, without its history (no version, no branch, no change), and its import as one change made of node creations
// and modifications, left for review like any other change.

// SnapshotFormat names the format of a graph snapshot (JSON).
const SnapshotFormat = "goap.graph.snapshot/v1"

// Snapshot is the state of the graph of a namespace at a baseline, without history: its nodes named by key, their
// outgoing links to the other nodes of the snapshot.
type Snapshot struct {
	Format    string `json:"format"`
	Namespace string `json:"namespace"`
	// Branch and Baseline say where the state was read; informative, an import ignores them.
	Branch     string         `json:"branch,omitempty"`
	Baseline   string         `json:"baseline,omitempty"`
	ExportedAt time.Time      `json:"exportedAt"`
	Nodes      []SnapshotNode `json:"nodes"`
}

// SnapshotNode is a node of a snapshot.
type SnapshotNode struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	State string `json:"state,omitempty"`
	// Owner is the key of the organisational unit owning the node.
	Owner      string         `json:"owner,omitempty"`
	Properties map[string]any `json:"props,omitempty"`
	Links      []SnapshotLink `json:"links,omitempty"`
	// Provenance says where the version exported comes from; informative, an import ignores it.
	Provenance *SnapshotProvenance `json:"provenance,omitempty"`
}

// SnapshotProvenance is the origin of the version of a node of a snapshot: the change that wrote it, the action or
// person that produced it, and whether the run that produced it called a model, the machine-readable mark of content an
// AI generated (EU AI Act Art. 50(2)). The whole story is the change's PROV-O export (ADR 0057).
type SnapshotProvenance struct {
	Version    int    `json:"version" yaml:"version"`
	Change     string `json:"change,omitempty" yaml:"change,omitempty"`
	ProducedBy string `json:"producedBy,omitempty" yaml:"producedBy,omitempty"`
	// Model is true when the action run that produced the version called a model.
	Model bool `json:"model,omitempty" yaml:"model,omitempty"`
}

// SnapshotLink is an outgoing link of a node of a snapshot, to another node of the snapshot named by its key.
type SnapshotLink struct {
	Type       string         `json:"type"`
	To         string         `json:"to"`
	Properties map[string]any `json:"props,omitempty"`
}

// ExportSnapshot reads the state of a namespace at a baseline (empty: the head of the branch, main by default) as a
// snapshot. The links kept are the ones between nodes of the namespace: a link to another namespace is left out.
func (g *Graph) ExportSnapshot(ctx context.Context, namespace, branch string, baseline domain.BaselineID) (Snapshot, error) {
	namespace = domain.NamespaceOf(namespace)
	branch = domain.BranchOf(branch)
	s := Snapshot{Format: SnapshotFormat, Namespace: namespace, Branch: branch, ExportedAt: g.now().UTC(), Nodes: []SnapshotNode{}}
	if baseline == "" {
		head, err := g.BranchHead(ctx, namespace, branch)
		if err != nil {
			return s, err
		}
		baseline = head.ID
	} else {
		b, err := g.Baseline(ctx, baseline)
		if err != nil {
			return s, err
		}
		if b.Namespace != namespace {
			return s, invalidf("baseline %s is a baseline of %s, not %s", baseline, b.Namespace, namespace)
		}
		s.Branch = domain.BranchOf(b.Branch)
	}
	s.Baseline = string(baseline)
	if baseline == "" {
		return s, nil // nothing committed yet: the empty state
	}
	nodes, links, err := g.BaselineGraph(ctx, baseline)
	if err != nil {
		return s, err
	}
	keys := map[domain.NodeID]string{}
	for _, n := range nodes {
		keys[n.ID] = n.Key
	}
	out := map[domain.NodeID][]SnapshotLink{}
	for _, l := range links {
		to, ok := keys[l.To.ID]
		if !ok {
			continue
		}
		out[l.From.ID] = append(out[l.From.ID], SnapshotLink{Type: l.Type, To: to, Properties: l.Properties})
	}
	provenance := g.provenanceReader(ctx)
	for _, n := range nodes {
		if n.Deleted {
			continue
		}
		sn := SnapshotNode{Key: n.Key, Type: n.Type, State: n.State, Properties: n.Properties, Links: out[n.ID], Provenance: provenance(n)}
		if n.Owner != "" {
			sn.Owner = n.Owner
		}
		slices.SortFunc(sn.Links, func(a, b SnapshotLink) int {
			if c := strings.Compare(a.Type, b.Type); c != 0 {
				return c
			}
			return strings.Compare(a.To, b.To)
		})
		s.Nodes = append(s.Nodes, sn)
	}
	slices.SortFunc(s.Nodes, func(a, b SnapshotNode) int { return strings.Compare(a.Key, b.Key) })
	return s, nil
}

// provenanceReader returns the provenance of a version, reading the impacts and the model calls of each change once.
func (g *Graph) provenanceReader(ctx context.Context) func(domain.Node) *SnapshotProvenance {
	type origin struct {
		impacts map[domain.ChangeImpactID]domain.ChangeImpact
		models  map[string]bool // the executions that called a model
	}
	cache := map[domain.ChangeID]*origin{}
	read := func(id domain.ChangeID) *origin {
		if o, ok := cache[id]; ok {
			return o
		}
		o := &origin{impacts: map[domain.ChangeImpactID]domain.ChangeImpact{}, models: map[string]bool{}}
		_ = g.repo.InTx(ctx, func(tx Tx) error {
			impacts, err := tx.ChangeImpacts(ctx, id)
			if err != nil {
				return err
			}
			for _, cn := range impacts {
				o.impacts[cn.ID] = cn
			}
			entries, err := tx.Log(ctx, domain.LogFilter{Change: id, Types: []string{domain.LogModel + "."}})
			if err != nil {
				return err
			}
			for _, e := range entries {
				if e.Execution != "" {
					o.models[e.Execution] = true
				}
			}
			return nil
		})
		cache[id] = o
		return o
	}
	return func(n domain.Node) *SnapshotProvenance {
		if n.ChangeID == "" {
			return nil
		}
		p := &SnapshotProvenance{Version: int(n.Version), Change: string(n.ChangeID)}
		if n.ChangeImpact != "" {
			o := read(n.ChangeID)
			if cn, ok := o.impacts[n.ChangeImpact]; ok {
				p.ProducedBy = cn.ProducedBy
				p.Model = cn.Execution != "" && o.models[cn.Execution]
			}
		}
		return p
	}
}

// ParseSnapshot decodes a JSON snapshot and checks its shape: the format, a namespace, the keys unique, every type of
// the namespace, every link to a node of the snapshot or of the graph (checked at import).
func ParseSnapshot(data []byte) (Snapshot, error) {
	var s Snapshot
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, invalidf("parse snapshot: %v", err)
	}
	return s, s.check()
}

func (s Snapshot) check() error {
	if s.Format != SnapshotFormat {
		return invalidf("unknown snapshot format %q (expected %q)", s.Format, SnapshotFormat)
	}
	if s.Namespace == "" {
		return invalidf("a snapshot names its namespace")
	}
	seen := map[string]bool{}
	for i, n := range s.Nodes {
		if n.Key == "" || n.Type == "" {
			return invalidf("node %d: a key and a type are required", i)
		}
		if seen[n.Key] {
			return invalidf("node %s appears twice", n.Key)
		}
		seen[n.Key] = true
		if q, err := domain.ParseTypeRef(n.Type); err != nil || (q.Namespace != "" && q.Namespace != s.Namespace) {
			return invalidf("node %s: type %q is not a type of namespace %s", n.Key, n.Type, s.Namespace)
		}
		for _, l := range n.Links {
			if l.Type == "" || l.To == "" {
				return invalidf("node %s: a link needs a type and a target", n.Key)
			}
		}
	}
	return nil
}

// SnapshotImport describes the import of a snapshot.
type SnapshotImport struct {
	Snapshot Snapshot
	// Title, Intent and Methodology of the change the import creates (a default title and intent when empty).
	Title, Intent, Methodology string
	// OwnerOrg and ProjectID are the unit holding the change and the project it acts in (the project is required).
	OwnerOrg, ProjectID string
	// By names the producer of the change impacts.
	By string
	// Gate, when set, is asked for every node type the import writes, once the change exists (authorization of the
	// edges): an error stops the import, and the change is purged.
	Gate func(c domain.Change, typ string) error
	// Progress, when set, is told how far the import is: done of total steps (a node written, then its links).
	Progress func(done, total int)
}

func (in SnapshotImport) progress(done, total int) {
	if in.Progress != nil {
		in.Progress(done, total)
	}
}

// SnapshotImportResult is the outcome of an import: the change it created (empty when the graph already held the
// snapshot: nothing to import), what it did and what it could not do.
type SnapshotImportResult struct {
	Change                      domain.ChangeID
	Created, Updated, Unchanged int
	// Warnings are what the import left out (an owner unknown here, a link to a node found nowhere, a lifecycle
	// state not reached by one transition).
	Warnings []string
}

// ImportSnapshot imports a snapshot into the main branch of its namespace as one change (ADR 0104): a node of the
// snapshot the graph does not hold is created, one it holds is checked out and brought to the snapshot (properties,
// owner, lifecycle state, outgoing links to the nodes of the namespace), one equal to the snapshot is left alone, and
// the nodes the snapshot does not name are untouched (a node is never deleted). The change stays active with its
// impacts proposed: it is reviewed, committed and merged like any other. A failed import leaves no change behind.
func (g *Graph) ImportSnapshot(ctx context.Context, in SnapshotImport) (res SnapshotImportResult, err error) {
	s := in.Snapshot
	if err := s.check(); err != nil {
		return res, err
	}
	if len(s.Nodes) == 0 {
		return res, invalidf("the snapshot holds no node")
	}
	ns := s.Namespace
	head, err := g.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		return res, err
	}
	var nodes []domain.Node
	var links []domain.Link
	if head.ID != "" {
		if nodes, links, err = g.BaselineGraph(ctx, head.ID); err != nil {
			return res, err
		}
	}
	byKey := map[string]domain.Node{}
	keyOf := map[domain.NodeID]string{}
	for _, n := range nodes {
		byKey[n.Key], keyOf[n.ID] = n, n.Key
	}
	outOf := map[domain.NodeID][]domain.Link{}
	for _, l := range links {
		outOf[l.From.ID] = append(outOf[l.From.ID], l)
	}
	inSnapshot := map[string]bool{}
	for _, n := range s.Nodes {
		inSnapshot[n.Key] = true
	}
	orgNS := g.Structure(domain.StructureOrganisation).Namespace
	knownUnit := map[string]bool{}
	unit := func(key string) bool {
		if key == "" {
			return false
		}
		if ok, seen := knownUnit[key]; seen {
			return ok
		}
		_, err := g.NodeByKey(ctx, orgNS, key)
		knownUnit[key] = err == nil
		return err == nil
	}
	warn := func(format string, args ...any) { res.Warnings = append(res.Warnings, fmt.Sprintf(format, args...)) }

	// what each node of the snapshot asks: a creation, a modification of the node held, or nothing
	type plan struct {
		sn      SnapshotNode
		node    *domain.Node // the node held (nil: created)
		props   map[string]any
		owner   string
		state   bool
		links   bool
		impact  domain.ChangeImpactID
		draftID domain.NodeID
	}
	var plans []*plan
	for _, sn := range s.Nodes {
		p := &plan{sn: sn}
		owner := sn.Owner
		if owner != "" && !unit(owner) {
			warn("%s: owner %s is not a unit of this graph, left to the unit of the change", sn.Key, owner)
			owner = ""
		}
		if n, ok := byKey[sn.Key]; ok {
			if n.Type != sn.Type {
				return res, invalidf("node %s is a %s here, the snapshot has a %s", sn.Key, n.Type, sn.Type)
			}
			p.node = &n
			p.props = propsPatch(n.Properties, sn.Properties)
			if owner != "" && n.Owner != owner {
				p.owner = owner
			}
			p.state = sn.State != "" && sn.State != n.State
			p.links = !sameLinks(outOf[n.ID], keyOf, sn.Links)
			if len(p.props) == 0 && p.owner == "" && !p.state && !p.links {
				res.Unchanged++
				continue
			}
		} else {
			p.owner = owner
		}
		plans = append(plans, p)
	}
	if len(plans) == 0 {
		return res, nil
	}

	title := in.Title
	if title == "" {
		title = fmt.Sprintf("Import of a snapshot of %s", ns)
	}
	intent := in.Intent
	if intent == "" {
		intent = fmt.Sprintf("Bring %s to the snapshot exported at %s", ns, s.ExportedAt.Format(time.RFC3339))
	}
	c, err := g.CreateChange(ctx, NewChange{Namespace: ns, Title: title, Intent: intent, Methodology: in.Methodology, OwnBranch: true,
		OwnerOrg: in.OwnerOrg, ProjectID: in.ProjectID})
	if err != nil {
		return res, err
	}
	defer func() {
		if err == nil {
			return
		}
		res.Change = ""
		if _, perr := g.PurgeChange(ctx, c.ID); perr != nil {
			abandoned := domain.ChangeAbandoned
			if _, aerr := g.UpdateChange(ctx, c.ID, ChangePatch{Status: &abandoned}); aerr != nil {
				err = errors.Join(err, aerr)
			}
		}
	}()
	if in.Gate != nil {
		types := map[string]bool{}
		for _, p := range plans {
			if !types[p.sn.Type] {
				types[p.sn.Type] = true
				if err := in.Gate(c, p.sn.Type); err != nil {
					return res, err
				}
			}
		}
	}
	why := "imported from a snapshot"

	// first the nodes, created or checked out, so that the links may name any of them
	for i, p := range plans {
		in.progress(i, 2*len(plans))
		if p.node == nil {
			cn, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: p.sn.Key, Type: p.sn.Type, Properties: p.sn.Properties, Owner: p.owner, Rationale: why, ProducedBy: in.By})
			if err != nil {
				return res, fmt.Errorf("%s: %w", p.sn.Key, err)
			}
			p.impact, p.draftID = cn.ID, cn.Post.ID
			keyOf[cn.Post.ID] = p.sn.Key
			res.Created++
			continue
		}
		cn, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: p.node.ID, Rationale: why, ProducedBy: in.By})
		if err != nil {
			return res, fmt.Errorf("%s: %w", p.sn.Key, err)
		}
		p.impact, p.draftID = cn.ID, p.node.ID
		if len(p.props) > 0 || p.owner != "" {
			if _, err := g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: p.props, Owner: p.owner}); err != nil {
				return res, fmt.Errorf("%s: %w", p.sn.Key, err)
			}
		}
		res.Updated++
	}
	// the target of a link: a node of the import (its draft), else a node the graph holds in the namespace
	refOf := map[string]domain.NodeRef{}
	for _, p := range plans {
		refOf[p.sn.Key] = domain.NodeRef{ID: p.draftID}
	}
	target := func(key string) (domain.NodeRef, bool) {
		if r, ok := refOf[key]; ok {
			return r, true
		}
		if n, ok := byKey[key]; ok {
			return n.Ref(), true
		}
		return domain.NodeRef{}, false
	}
	for i, p := range plans {
		in.progress(len(plans)+i, 2*len(plans))
		if p.node != nil && !p.links {
			continue
		}
		var held []domain.Link
		if p.node != nil {
			v, err := g.ChangeNodeView(ctx, c.ID, "", domain.NodeRef{ID: p.draftID})
			if err != nil {
				return res, fmt.Errorf("%s: %w", p.sn.Key, err)
			}
			held = v.Out
		}
		matched := make([]bool, len(held))
		for _, l := range p.sn.Links {
			to, ok := target(l.To)
			if !ok {
				warn("%s: link %s to %s left out: no such node, neither in the snapshot nor in %s", p.sn.Key, l.Type, l.To, ns)
				continue
			}
			found := false
			for i, h := range held {
				if matched[i] || h.Type != l.Type || h.To.ID != to.ID {
					continue
				}
				matched[i], found = true, true
				if !sameValue(h.Properties, l.Properties) {
					if _, err := g.ImpactLinkUpdate(ctx, c.ID, h.ID, l.Properties, "", ""); err != nil {
						return res, fmt.Errorf("%s: link %s to %s: %w", p.sn.Key, l.Type, l.To, err)
					}
				}
				break
			}
			if found {
				continue
			}
			if _, err := g.ImpactLinkCreate(ctx, c.ID, p.impact, LinkWrite{Type: l.Type, To: to, Properties: l.Properties}, "", ""); err != nil {
				return res, fmt.Errorf("%s: link %s to %s: %w", p.sn.Key, l.Type, l.To, err)
			}
		}
		for i, h := range held {
			// a link to a node outside the namespace is not part of a snapshot: it stays
			if matched[i] || !inNamespace(keyOf, h.To.ID) {
				continue
			}
			if err := g.ImpactLinkDelete(ctx, c.ID, h.ID, "", ""); err != nil {
				return res, fmt.Errorf("%s: link %s: %w", p.sn.Key, h.Type, err)
			}
		}
	}
	// the lifecycle states last, over the drafts: one transition, else a warning
	for _, p := range plans {
		if p.sn.State == "" {
			continue
		}
		cur, err := g.ChangeNode(ctx, c.ID, "", domain.NodeRef{ID: p.draftID})
		if err != nil {
			return res, fmt.Errorf("%s: %w", p.sn.Key, err)
		}
		if cur.State == p.sn.State {
			continue
		}
		if _, err := g.ImpactNodeTransition(ctx, c.ID, NodeTransition{NodeCheckout: NodeCheckout{Impact: p.impact}, To: p.sn.State}); err != nil {
			if errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) {
				warn("%s: state %s not reached from %s (%v)", p.sn.Key, p.sn.State, cur.State, err)
				continue
			}
			return res, fmt.Errorf("%s: %w", p.sn.Key, err)
		}
	}
	res.Change = c.ID
	return res, nil
}

func inNamespace(keyOf map[domain.NodeID]string, id domain.NodeID) bool {
	_, ok := keyOf[id]
	return ok
}

// propsPatch is the patch bringing the properties held to the wanted ones: the values that differ, nil for the ones
// the wanted leave out.
func propsPatch(held, want map[string]any) map[string]any {
	patch := map[string]any{}
	for k, v := range want {
		if hv, ok := held[k]; !ok || !sameValue(hv, v) {
			patch[k] = v
		}
	}
	for k := range held {
		if _, ok := want[k]; !ok {
			patch[k] = nil
		}
	}
	return patch
}

// sameLinks tells whether the outgoing links held to nodes of the namespace are the wanted ones (type, target key and
// properties, as multisets).
func sameLinks(held []domain.Link, keyOf map[domain.NodeID]string, want []SnapshotLink) bool {
	var h []SnapshotLink
	for _, l := range held {
		if k, ok := keyOf[l.To.ID]; ok {
			h = append(h, SnapshotLink{Type: l.Type, To: k, Properties: l.Properties})
		}
	}
	if len(h) != len(want) {
		return false
	}
	used := make([]bool, len(h))
	for _, w := range want {
		found := false
		for i, x := range h {
			if !used[i] && x.Type == w.Type && x.To == w.To && sameValue(x.Properties, w.Properties) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// sameValue compares two values as JSON would carry them (numbers, empty maps).
func sameValue(a, b any) bool {
	return reflect.DeepEqual(normValue(a), normValue(b))
}

func normValue(v any) any {
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return v
	}
	if m, ok := out.(map[string]any); ok {
		if len(m) == 0 {
			return nil
		}
		return maps.Clone(m)
	}
	return out
}
