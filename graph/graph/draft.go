package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// Reading the drafts of a change (ADR 0079). A draft is the working state of a node in a change, per change impact and
// flow, the fold of the impact log (Graph.drafts). What a flow sees of a node of the change is the draft of the
// innermost flow of its chain that holds one (the flow, its ancestors, the main flow); a draft is its flow's to edit, a
// child flow checks the node out to write its own. Outside the change a node is still its last committed version.

// draftNode is the draft seen as a node: no version (Version 0), the namespace of the change, the branch of its flow.
func draftNode(c domain.Change, d domain.Draft) domain.Node {
	n := d.AsNode()
	n.Namespace = domain.NamespaceOf(c.Namespace)
	n.Branch = draftBranch(c, d.Flow)
	return n
}

// ownDraft is the draft of a flow itself (nil: none) among the rows of a change.
func ownDraft(rows []domain.Draft, impact domain.ChangeImpactID, flow string) *domain.Draft {
	for _, d := range rows {
		if d.Impact == impact && d.Flow == flow {
			c := d.Clone()
			return &c
		}
	}
	return nil
}

func flowIDs(chain []domain.Flow) []string {
	out := make([]string, len(chain))
	for i, f := range chain {
		out[i] = f.ID
	}
	return out
}

// seenDrafts are the drafts a process on a flow sees, by change impact: the rows of the innermost flow of the chain
// holding one, or, when runs are stale on the chain, what the events of the others left without them.
func (g *Graph) seenDrafts(ctx context.Context, tx Tx, c domain.Change, flow string) (map[domain.ChangeImpactID]domain.Draft, error) {
	rows, err := g.drafts(ctx, tx, c.ID)
	if err != nil {
		return nil, err
	}
	chain := flowChain(c, flow)
	stale := staleOf(chain)
	ids := flowIDs(chain)
	out := map[domain.ChangeImpactID]domain.Draft{}
	if len(stale) == 0 {
		for _, f := range append(slices.Clone(ids), "") {
			for _, d := range rows {
				if _, ok := out[d.Impact]; !ok && d.Flow == f {
					out[d.Impact] = d
				}
			}
		}
		return out, nil
	}
	events, err := g.foldedEvents(ctx, tx, c.ID)
	if err != nil {
		return nil, err
	}
	isStale := func(e string) bool { return e != "" && stale[e] }
	for _, cn := range c.Impacts().All() {
		if d := domain.DraftSeenBy(events, cn.ID, ids, isStale); d != nil {
			out[cn.ID] = *d
		}
	}
	return out, nil
}

// seenDraft is the draft a flow sees for a change impact (nil: none).
func (g *Graph) seenDraft(ctx context.Context, tx Tx, c domain.Change, flow string, impact domain.ChangeImpactID) (*domain.Draft, error) {
	d, shared, err := g.heldSeenDraft(ctx, tx, c, flow, impact)
	if shared && d != nil {
		c := d.Clone()
		d = &c
	}
	return d, err
}

// heldSeenDraft is seenDraft for a reader that changes nothing of the draft: shared, the view's (read only).
func (g *Graph) heldSeenDraft(ctx context.Context, tx Tx, c domain.Change, flow string, impact domain.ChangeImpactID) (*domain.Draft, bool, error) {
	if chain := flowChain(c, flow); len(staleOf(chain)) == 0 {
		// the draft of the innermost flow of the chain holding one, as seenDrafts finds it, without copying the others
		st, err := g.draftState(ctx, tx, c.ID)
		if err != nil {
			return nil, false, err
		}
		for _, f := range append(flowIDs(chain), "") {
			if d, ok := st.draft(impact, f); ok {
				return &d, true, nil
			}
		}
		return nil, false, nil
	}
	m, err := g.seenDrafts(ctx, tx, c, flow)
	if err != nil {
		return nil, false, err
	}
	if d, ok := m[impact]; ok {
		return &d, false, nil
	}
	return nil, false, nil
}

// draftReader reads nodes as a flow of a change sees them: a draft reference (a node the flow holds a draft of) is the
// draft, any other reference a stored version. It finds the drafts it is asked for as it is asked (a check of one draft
// reads the few its links name), and gathers them all only for what goes over every draft (sortedDrafts).
type draftReader struct {
	c domain.Change
	// byNode are the drafts read so far, and the ones a caller set (put)
	byNode map[domain.NodeID]domain.Draft
	// lazy finds the draft of a node, all gathers them (nil once byNode holds them all)
	lazy func(domain.NodeID) (domain.Draft, bool)
	all  func() map[domain.NodeID]domain.Draft
	// in are the stored links entering versions, latest the latest version of nodes on main (0: none), read in one
	// batch by prefetch
	in     map[domain.NodeRef][]domain.Link
	latest map[domain.NodeID]domain.Version
}

// prefetch reads in batches what viewIn reads of the stored versions refs: the versions and their links, the links
// entering them, the latest version of their nodes on main.
func (r *draftReader) prefetch(ctx context.Context, tx Tx, refs []domain.NodeRef) error {
	if err := prefetchVersions(ctx, tx, refs); err != nil {
		return err
	}
	var exact []domain.NodeRef
	var ids []domain.NodeID
	seen := map[domain.NodeID]bool{}
	for _, ref := range refs {
		if !ref.IsDraft() {
			exact = append(exact, ref)
		}
		if !seen[ref.ID] {
			seen[ref.ID] = true
			ids = append(ids, ref.ID)
		}
	}
	ls, err := tx.LinksInto(ctx, exact)
	if err != nil {
		return err
	}
	if r.in == nil {
		r.in = map[domain.NodeRef][]domain.Link{}
	}
	for _, ref := range exact {
		r.in[ref] = nil
	}
	for _, l := range ls {
		r.in[l.To] = append(r.in[l.To], l)
	}
	latest, err := tx.LatestVersionsOn(ctx, ids, domain.MainBranch)
	if err != nil {
		return err
	}
	if r.latest == nil {
		r.latest = map[domain.NodeID]domain.Version{}
	}
	for _, id := range ids {
		r.latest[id] = latest[id]
	}
	return nil
}

// newDraftReader reads the drafts a flow sees, for the change impacts it sees (visible: stale or foreign impacts leave
// their drafts out).
func (g *Graph) newDraftReader(ctx context.Context, tx Tx, c domain.Change, flow string, visible domain.Impacts) (*draftReader, error) {
	r := &draftReader{c: c, byNode: map[domain.NodeID]domain.Draft{}}
	chain := flowChain(c, flow)
	if len(staleOf(chain)) > 0 {
		seen, err := g.seenDrafts(ctx, tx, c, flow)
		if err != nil {
			return nil, err
		}
		for _, cn := range visible.All() {
			if d, ok := seen[cn.ID]; ok {
				r.byNode[d.Node] = d
			}
		}
		return r, nil
	}
	st, err := g.draftState(ctx, tx, c.ID)
	if err != nil {
		return nil, err
	}
	if ownDrafts(tx, c.ID) == st {
		st.frozen = true // the reader reads it later: the transaction's next fold is a copy
	}
	order := append(flowIDs(chain), "")
	pick := func(id domain.ChangeImpactID) (domain.Draft, bool) {
		for _, f := range order {
			if d, ok := st.draft(id, f); ok {
				return d, true
			}
		}
		return domain.Draft{}, false
	}
	pos := make(map[domain.ChangeImpactID]int, visible.Len())
	for i, cn := range visible.All() {
		pos[cn.ID] = i
	}
	// as the whole reader would hold it: the draft of the last visible impact of the node
	r.lazy = func(node domain.NodeID) (domain.Draft, bool) {
		best, out := -1, domain.Draft{}
		held, _ := st.nodes.get(node)
		for _, id := range held {
			if i, ok := pos[id]; ok && i > best {
				if d, ok := pick(id); ok && d.Node == node {
					best, out = i, d
				}
			}
		}
		if best < 0 {
			return domain.Draft{}, false
		}
		return out.Clone(), true
	}
	r.all = func() map[domain.NodeID]domain.Draft {
		m := map[domain.NodeID]domain.Draft{}
		for _, cn := range visible.All() {
			if d, ok := pick(cn.ID); ok {
				m[d.Node] = d.Clone()
			}
		}
		return m
	}
	return r, nil
}

// get is the draft the reader holds for a node.
func (r *draftReader) get(id domain.NodeID) (domain.Draft, bool) {
	if d, ok := r.byNode[id]; ok {
		return d, true
	}
	if r.lazy == nil {
		return domain.Draft{}, false
	}
	d, ok := r.lazy(id)
	if ok {
		r.byNode[id] = d
	}
	return d, ok
}

// put makes the reader hold a draft for its node (in place of the one it found, if any).
func (r *draftReader) put(d domain.Draft) { r.byNode[d.Node] = d }

// whole is every draft the reader holds, by node.
func (r *draftReader) whole() map[domain.NodeID]domain.Draft {
	if r.lazy != nil {
		m := r.all()
		maps.Copy(m, r.byNode) // what was read is the same, what was put stays
		r.byNode, r.lazy, r.all = m, nil, nil
	}
	return r.byNode
}

// draftNode is the node of a draft the reader holds.
func (r *draftReader) draftNode(d domain.Draft) domain.Node { return draftNode(r.c, d) }

// draft is the draft a reference designates: a draft reference, or an exact version the draft was checked out from.
func (r *draftReader) draft(ref domain.NodeRef) (domain.Draft, bool) {
	d, ok := r.get(ref.ID)
	if !ok || (!ref.IsDraft() && (d.Base == nil || *d.Base != ref)) {
		return domain.Draft{}, false
	}
	return d, true
}

// normalize points a reference to the version a node was checked out from at the draft of the node.
func (r *draftReader) normalize(ref domain.NodeRef) domain.NodeRef {
	if d, ok := r.get(ref.ID); ok && d.Base != nil && *d.Base == ref {
		return d.Ref()
	}
	return ref
}

// node reads a node: the draft for a draft reference, else the stored version.
func (r *draftReader) node(ctx context.Context, tx Tx, ref domain.NodeRef) (domain.Node, error) {
	if ref.IsDraft() {
		if d, ok := r.get(ref.ID); ok {
			return r.draftNode(d), nil
		}
	}
	return tx.Node(ctx, ref)
}

// outLinks reads the outgoing links of a node: the ones of its draft for a draft reference, else the stored ones. The
// targets a draft holds as the version a node it also holds was checked out from are shown as that draft.
func (r *draftReader) outLinks(ctx context.Context, tx Tx, ref domain.NodeRef) ([]domain.Link, error) {
	if ref.IsDraft() {
		if d, ok := r.get(ref.ID); ok {
			out := d.OutLinks()
			for i := range out {
				out[i].To = r.normalize(out[i].To)
			}
			return out, nil
		}
	}
	return tx.OutLinks(ctx, ref)
}

// inLinks reads the links entering a node: the stored ones of the version, and the ones the drafts of the flow hold to
// it.
func (r *draftReader) inLinks(ctx context.Context, tx Tx, ref domain.NodeRef) ([]domain.Link, error) {
	var out []domain.Link
	from := ref
	if d, ok := r.get(ref.ID); ok && ref.IsDraft() && d.Base != nil {
		from = *d.Base // the links of the version the draft was checked out from still enter the node
	}
	if !from.IsDraft() {
		stored, ok := r.in[from]
		if !ok {
			var err error
			if stored, err = tx.InLinks(ctx, from); err != nil {
				return nil, err
			}
		}
		for _, l := range stored {
			// a link of a version a draft of the flow replaces is the draft's now
			if _, ok := r.draft(l.From); ok && !l.From.IsDraft() {
				continue
			}
			out = append(out, l)
		}
	}
	for _, d := range r.sortedDrafts() {
		for _, l := range d.Links {
			to := r.normalize(l.To)
			if to.ID == ref.ID && (ref.IsDraft() == to.IsDraft()) && (ref.IsDraft() || to == ref) {
				out = append(out, domain.Link{ID: l.ID, Type: l.Type, From: d.Ref(), To: to, Properties: l.Properties, ChangeID: d.Change})
			}
		}
	}
	return out, nil
}

func (r *draftReader) sortedDrafts() []domain.Draft {
	all := r.whole()
	out := make([]domain.Draft, 0, len(all))
	for _, d := range all {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b domain.Draft) int {
		if a.Key != b.Key {
			if a.Key < b.Key {
				return -1
			}
			return 1
		}
		return 0
	})
	return out
}

// ChangeNodeView reads a node as a call of a change on a flow sees it (ADR 0079): for a draft reference (Version 0) the
// draft the flow holds for the node, hydrated with its links, else the stored version (Version 0 with no draft: the
// latest on main).
func (g *Graph) ChangeNodeView(ctx context.Context, id domain.ChangeID, flow string, ref domain.NodeRef) (v domain.NodeView, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		flow := c.ResolveFlow(flow)
		seen, err := g.newFlowNodes(tx, c, flow).nodes(ctx)
		if err != nil {
			return err
		}
		r, err := g.newDraftReader(ctx, tx, c, flow, seen)
		if err != nil {
			return err
		}
		v, err = viewIn(ctx, tx, r, ref)
		return err
	})
	return
}

// LinkSourceType is the type of the node an outgoing link of a draft of the change leaves (or, failing that, the stored
// link of that id): what the gate of the access nodes asks before a link of a draft is edited (ADR 0079).
func (g *Graph) LinkSourceType(ctx context.Context, id domain.ChangeID, link domain.LinkID) (typ string, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		st, err := g.draftState(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, rows := range st.drafts.all() {
			for _, d := range rows {
				if _, ok := d.Link(link); ok {
					typ = d.Type
					return nil
				}
			}
		}
		l, err := tx.Link(ctx, link)
		if err != nil {
			return err
		}
		n, err := tx.Node(ctx, l.From)
		typ = n.Type
		return err
	})
	return
}

// ChangeNodeViewByKey is ChangeNodeView for the node a key designates: the one the change holds a draft of on the flow,
// else the latest version of the node on main.
func (g *Graph) ChangeNodeViewByKey(ctx context.Context, id domain.ChangeID, flow, namespace, key string) (v domain.NodeView, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		flow := c.ResolveFlow(flow)
		seen, err := g.newFlowNodes(tx, c, flow).nodes(ctx)
		if err != nil {
			return err
		}
		r, err := g.newDraftReader(ctx, tx, c, flow, seen)
		if err != nil {
			return err
		}
		ref := domain.NodeRef{}
		for _, d := range r.sortedDrafts() {
			if d.Key == key && domain.NamespaceOf(c.Namespace) == domain.NamespaceOf(namespace) {
				ref = d.Ref()
			}
		}
		if ref.ID == "" && domain.NamespaceOf(c.Namespace) == domain.NamespaceOf(namespace) {
			// a node the change committed, or a sub-change merged into it (ADR 0103): the version it committed
			for _, cn := range seen.All() {
				if cn.Key == key && cn.Committed != nil {
					ref = *cn.Committed
				} else if cn.Key == key && cn.Post != nil && !cn.Post.IsDraft() {
					ref = *cn.Post
				}
			}
		}
		if ref.ID == "" {
			n, err := tx.NodeByKey(ctx, namespace, key)
			if err != nil {
				return err
			}
			ref = n.Ref()
			// a change with a branch of its own reads the version its branch holds: what its parent committed, for a
			// sub-change (ADR 0103), not the latest version anywhere
			if own, ok, err := ownBranch(ctx, tx, c); err != nil {
				return err
			} else if ok {
				head, err := branchVersions(ctx, tx, own, []domain.NodeID{n.ID})
				if err != nil {
					return err
				}
				if at, ok := head[n.ID]; ok {
					ref.Version = at
				}
			}
		}
		v, err = viewIn(ctx, tx, r, ref)
		return err
	})
	return
}

// ChangeNode is ChangeNodeView without the neighbourhood.
func (g *Graph) ChangeNode(ctx context.Context, id domain.ChangeID, flow string, ref domain.NodeRef) (domain.Node, error) {
	v, err := g.ChangeNodeView(ctx, id, flow, ref)
	return v.Node, err
}

// viewIn hydrates a node with its neighbourhood as a draft reader sees it.
func viewIn(ctx context.Context, tx Tx, r *draftReader, ref domain.NodeRef) (domain.NodeView, error) {
	n, err := r.node(ctx, tx, ref)
	if err != nil {
		return domain.NodeView{}, err
	}
	var latest domain.Version
	if v, ok := r.latest[ref.ID]; ok {
		latest = v
	} else {
		l, err := tx.Node(ctx, domain.NodeRef{ID: ref.ID})
		if err != nil && !isNotFound(err) {
			return domain.NodeView{}, err
		}
		latest = l.Version
	}
	if latest == 0 {
		latest = n.Version // a node the change creates, not on main yet
	}
	out, err := r.outLinks(ctx, tx, ref)
	if err != nil {
		return domain.NodeView{}, err
	}
	in, err := r.inLinks(ctx, tx, ref)
	if err != nil {
		return domain.NodeView{}, err
	}
	return domain.NodeView{Node: n, Latest: latest, Out: out, In: in}, nil
}

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// draftLinkTargetType is the type of the node a link of a draft targets: a draft of the flow, or a stored version.
func (g *Graph) draftLinkTargetType(ctx context.Context, tx Tx, w *work, to domain.NodeRef) (string, error) {
	if to.IsDraft() {
		for _, cn := range w.seen.All() {
			if cn.Post != nil && cn.Post.IsDraft() && cn.Post.ID == to.ID {
				return cn.Type, nil
			}
		}
		return "", invalidf("a link targets the draft of node %s, which the change holds no draft of on this flow", to.ID)
	}
	n, err := tx.Node(ctx, to)
	if err != nil {
		return "", err
	}
	return n.Type, nil
}

// newDraftLink builds the outgoing link a draft gains: the type and the ends are checked, the target (an exact version,
// or the draft of a node of the change) is pointed to the draft of the node when it was checked out from that version.
func (g *Graph) newDraftLink(ctx context.Context, tx Tx, w *work, from domain.Draft, l LinkWrite) (domain.DraftLink, error) {
	if l.To.ID == "" && !l.Target.IsZero() && l.Type != "" {
		ext, err := g.targetOf(ctx, tx, w, from, &l)
		if err != nil {
			return domain.DraftLink{}, err
		}
		if ext != nil {
			return *ext, nil
		}
	}
	if l.Type == "" || l.To.ID == "" {
		return domain.DraftLink{}, invalidf("a link needs a type and a target")
	}
	toType, err := g.draftLinkTargetType(ctx, tx, w, l.To)
	if err != nil {
		return domain.DraftLink{}, err
	}
	if !l.To.IsDraft() && l.To.Version == 0 {
		return domain.DraftLink{}, invalidf("a link needs an exact target version, or the draft of a node of the change")
	}
	if err := w.ix.checkLink(l.Type, from.Type, toType); err != nil {
		return domain.DraftLink{}, err
	}
	if err := w.ix.checkLinkAttributes(l.Type, l.Properties); err != nil {
		return domain.DraftLink{}, err
	}
	return domain.DraftLink{ID: domain.LinkID(g.newID()), Type: l.Type, To: w.normalizeTo(l.To), Properties: l.Properties}, nil
}

// normalizeTo points the version a node of the flow was checked out from at its draft.
func (w *work) normalizeTo(to domain.NodeRef) domain.NodeRef {
	for _, cn := range w.seen.OfNode(to.ID) {
		if cn.Post != nil && cn.Post.IsDraft() && cn.Post.ID == to.ID && cn.Pre != nil && *cn.Pre == to {
			return *cn.Post
		}
	}
	return to
}

// draftFrom is the draft of a stored version a checkout event copies: the version as is, its outgoing links with ids
// derived from the event's (domain.BaseLinkID), so the log records the version only (ADR 0142).
func (g *Graph) draftFrom(w *work, event string, base domain.Node, links []domain.Link, execution string) (domain.Draft, *domain.BaseCopy) {
	cp := &domain.BaseCopy{}
	for _, l := range links {
		if to := w.normalizeTo(l.To); to != l.To && !slices.Contains(cp.Drafted, l.To) {
			cp.Drafted = append(cp.Drafted, l.To)
		}
	}
	e := domain.ImpactEvent{ID: event, Change: w.c.ID, Impact: w.cn.ID, Flow: w.flow, Execution: execution, FromBase: cp}
	return domain.CopyDraft(e, base, links), cp
}

// forkDraft is the draft a flow starts from when it checks a node out that a parent flow holds a draft of: a copy, the
// links with ids of their own.
func (g *Graph) forkDraft(w *work, parent domain.Draft, execution string) domain.Draft {
	d := parent.Clone()
	d.Change, d.Impact, d.Flow, d.Execution = w.c.ID, w.cn.ID, w.flow, execution
	for i := range d.Links {
		d.Links[i].ID = domain.LinkID(g.newID())
	}
	return d
}

func cloneMap(m map[string]any) map[string]any { return maps.Clone(m) }

// draftOf names the draft of a change impact in an error message.
func draftOf(cn domain.ChangeImpact) string {
	return fmt.Sprintf("change impact %s (%s)", cn.ID, cn.Key)
}
