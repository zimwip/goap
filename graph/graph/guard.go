package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
)

// The guard (ADR 0054) is the rule of the graph, in Go, whatever the storage: a database with constraints repeats
// it, a storage without any still gets it. Every repository transaction of a Graph goes through it:
//
//   - every node version, link, branch membership and node origin names the change that writes it, and that change
//     exists;
//   - every node version is owned by an organisational unit and every node was created in a project, nodes of the
//     structures in force; a version that names neither gets them from the version it follows, else from the change
//     that writes it (the unit holding it, the project it acts in); the project of a node never changes;
//   - every change names the unit holding it and the project it acts in, nodes of the structures in force;
//   - every baseline is the result of a change (ADR 0056); what precedes the first change of a namespace is the empty
//     state, the empty baseline id, which nothing stores;
//   - a version is immutable once written (ADR 0079): the working state of a node in a change is a draft, the version is
//     written when the change lands, with its outgoing links, in the same transaction; no version is ever edited,
//     frozen or dropped, and only a version written in the transaction gets links.
//
// What names a change, a unit or a project is checked when the transaction ends, before it commits (as a deferred
// foreign key): the bootstrap writes the root unit and the root project, each referencing the other and itself, in
// one transaction.

type guardRepo struct {
	Repo
	g *Graph
}

func (r *guardRepo) InTx(ctx context.Context, fn func(tx Tx) error) error {
	var gt *guardTx
	err := r.Repo.InTx(ctx, func(tx Tx) error {
		gt = &guardTx{Tx: tx, g: r.g}
		if err := fn(gt); err != nil {
			return err
		}
		return gt.verify(ctx)
	})
	if gt != nil && gt.purged {
		r.g.versions.reset() // what the transaction deleted may have been read meanwhile
	}
	if err == nil && len(gt.checkpoints) > 0 {
		r.g.checkpoints.store(r.g, gt.checkpoints)
	}
	if err == nil {
		// what the transaction folded of the logs it wrote is committed now: the next one folds only what follows
		for _, st := range gt.drafts {
			r.g.draftStates.put(st, r.g.draftCacheSize())
		}
	}
	return err
}

type guardTx struct {
	Tx
	g *Graph
	// what the transaction wrote that names a change, a unit or a project, checked at its end
	changeRefs map[domain.ChangeID]string
	owners     map[domain.NodeID]string
	projects   map[domain.NodeID]string
	changes    map[domain.ChangeID]domain.Change
	// newBaselines are the baselines the transaction wrote (their headers are not cached before it ends)
	newBaselines map[domain.BaselineID]bool
	// baselineReads are the versions the transaction read of some nodes in baselines it did not write (0: not held)
	baselineReads map[domain.BaselineID]map[domain.NodeID]domain.Version
	// forks are the baselines the lasting branches read by the transaction were forked from, by namespace and branch
	forks map[string]domain.BaselineID
	// landing is, per branch, the order of the baseline the versions the transaction writes or joins there land in
	// (ADR 0140): taken by the first of them, given to the baseline written after them
	landing map[string]int64
	// checkpoints are the headers the transaction wrote whose whole state is to be stored once it is over
	checkpoints []domain.BaselineID
	// headers caches the changes read to stamp versions
	headers map[domain.ChangeID]domain.Change
	// written are the versions the transaction wrote
	written map[domain.NodeRef]bool
	// nodes are the versions it read that it did not write (versioncache.go)
	nodes map[domain.NodeRef]domain.Node
	// purged: the transaction deleted a change, and versions with it (the version cache is emptied once it is over)
	purged bool
	// logged are the changes whose log the transaction appended to (the draft cache, ADR 0079, never publishes what a
	// transaction that may roll back has seen of its own writes)
	logged map[domain.ChangeID]bool
	// drafts are the folds of the logs the transaction appended to, as it sees them (never published to the draft cache)
	drafts map[domain.ChangeID]*draftState
	// impactSeq is, per change, the seq of the last impact event or fact the transaction appended (what the fold holds)
	impactSeq map[domain.ChangeID]int64
	// fresh is, per change whose lock the transaction holds, the fold it last read the log for: only its own appends can
	// follow it (ADR 0127)
	fresh map[domain.ChangeID]*draftState
	// reads are the changes read whole by the transaction, kept up to date by its own writes (a batch of N writes to
	// one change reads its N impacts once)
	reads map[domain.ChangeID]domain.Change
	// heads are the headers of the changes read by the transaction, their impacts taken from the fold of their log
	heads map[domain.ChangeID]domain.Change
	// locked are the changes and branches whose lock the transaction holds (ADR 0127)
	locked map[string]bool
}

// LockChange takes the lock of the tree of a change once per transaction (ADR 0127). Every write naming a change takes
// it (the log, the header, the projection of the impacts); an operation that reads before it writes takes it first,
// so that what it checked still holds when it writes.
func (t *guardTx) LockChange(ctx context.Context, id domain.ChangeID) error {
	if id == "" || t.locked["change:"+string(id)] {
		return nil
	}
	if err := t.Tx.LockChange(ctx, id); err != nil {
		return err
	}
	t.lock("change:" + string(id))
	return nil
}

// LockBranch takes the lock of a branch once per transaction (ADR 0127).
func (t *guardTx) LockBranch(ctx context.Context, namespace, name string) error {
	key := "branch:" + domain.NamespaceOf(namespace) + "/" + name
	if t.locked[key] {
		return nil
	}
	if err := t.Tx.LockBranch(ctx, namespace, name); err != nil {
		return err
	}
	t.lock(key)
	return nil
}

func (t *guardTx) lock(key string) {
	if t.locked == nil {
		t.locked = map[string]bool{}
	}
	t.locked[key] = true
}

func (t *guardTx) PutChangeImpact(ctx context.Context, change domain.ChangeID, cn domain.ChangeImpact) error {
	if err := t.LockChange(ctx, change); err != nil {
		return err
	}
	if err := t.Tx.PutChangeImpact(ctx, change, cn); err != nil {
		return err
	}
	c, ok := t.reads[change]
	if !ok {
		return nil
	}
	at := slices.IndexFunc(c.Nodes, func(n domain.ChangeImpact) bool { return n.ID == cn.ID })
	if at < 0 {
		delete(t.reads, change) // a new one: read again, as the store orders and stamps it
		return nil
	}
	// what the store gives back (its encoding), its creation time kept as an update keeps it
	r, err := toCNRow(cn)
	if err != nil {
		return err
	}
	stored, err := r.node()
	if err != nil {
		return err
	}
	stored.CreatedAt = c.Nodes[at].CreatedAt
	c.Nodes[at] = stored
	return nil
}

func (t *guardTx) DeleteChangeImpact(ctx context.Context, change domain.ChangeID, id domain.ChangeImpactID) error {
	if err := t.LockChange(ctx, change); err != nil {
		return err
	}
	if err := t.Tx.DeleteChangeImpact(ctx, change, id); err != nil {
		return err
	}
	if c, ok := t.reads[change]; ok {
		c.Nodes = slices.DeleteFunc(c.Nodes, func(n domain.ChangeImpact) bool { return n.ID == id })
		t.reads[change] = c
	}
	return nil
}

// changeHeaderReader is a store that reads a change without its impacts.
type changeHeaderReader interface {
	changeHeader(ctx context.Context, id domain.ChangeID) (domain.Change, error)
}

// Change reads a change: its header once per transaction, its change impacts from the fold of its log (the projection
// they are the same as, ADR 0029), so that an operation on a change of N impacts decodes none of them. A store that
// cannot read the header alone is read once per transaction, later reads being copies kept up to date by its writes.
func (t *guardTx) Change(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	_, ok := t.Tx.(changeHeaderReader)
	if !ok {
		if c, ok := t.reads[id]; ok {
			return copyChange(c), nil
		}
		c, err := t.Tx.Change(ctx, id)
		if err != nil {
			return c, err
		}
		if t.reads == nil {
			t.reads = map[domain.ChangeID]domain.Change{}
		}
		t.reads[id] = copyChange(c)
		return c, nil
	}
	c, ok := t.heads[id]
	if !ok {
		var err error
		if c, err = t.Tx.ChangeHead(ctx, id); err != nil {
			return c, err
		}
		if t.heads == nil {
			t.heads = map[domain.ChangeID]domain.Change{}
		}
		t.heads[id] = c
	}
	st, err := t.g.draftState(ctx, t, id)
	if err != nil {
		return c, err
	}
	c.Data = maps.Clone(c.Data)
	c.Guardians = slices.Clone(c.Guardians)
	c.DependsOn = slices.Clone(c.DependsOn)
	c.Items = slices.Clone(st.items)
	c.Nodes = slices.Clone(st.impacts) // values the fold replaces, never edits
	return c, nil
}

// copyChange is a copy of a change its reader may edit.
func copyChange(c domain.Change) domain.Change {
	c.Data = maps.Clone(c.Data)
	c.Guardians = slices.Clone(c.Guardians)
	c.DependsOn = slices.Clone(c.DependsOn)
	c.Items = slices.Clone(c.Items)
	c.Nodes = slices.Clone(c.Nodes)
	for i, cn := range c.Nodes {
		cn.Reviews = slices.Clone(cn.Reviews)
		cn.DerivedFrom = slices.Clone(cn.DerivedFrom)
		cn.Items = slices.Clone(cn.Items)
		for _, r := range []**domain.NodeRef{&cn.Pre, &cn.Post, &cn.Landed} {
			if *r != nil {
				v := **r
				*r = &v
			}
		}
		if cn.Validation != nil {
			v := *cn.Validation
			cn.Validation = &v
		}
		c.Nodes[i] = cn
	}
	return c
}

func (t *guardTx) AppendLog(ctx context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	if err := t.LockChange(ctx, e.Change); err != nil {
		return e, err
	}
	if t.logged == nil {
		t.logged = map[domain.ChangeID]bool{}
	}
	t.logged[e.Change] = true
	if !strings.HasPrefix(e.Type, domain.LogImpact+".") {
		delete(t.reads, e.Change) // a fact is an item of the change
		delete(t.heads, e.Change)
	}
	out, err := t.Tx.AppendLog(ctx, e)
	if err == nil && (strings.HasPrefix(e.Type, domain.LogImpact+".") || strings.HasPrefix(e.Type, domain.LogFact+".")) {
		if t.impactSeq == nil {
			t.impactSeq = map[domain.ChangeID]int64{}
		}
		t.impactSeq[e.Change] = out.Seq
	}
	return out, err
}

func (t *guardTx) wroteLog(id domain.ChangeID) bool { return t.logged[id] }

func (t *guardTx) ownDrafts(id domain.ChangeID) *draftState { return t.drafts[id] }

// current is the fold of a change that holds every impact event of its log, when the transaction knows one: it holds the
// lock of the change since it read the log (ADR 0127), so only its own appends can follow, and it has folded them.
func (t *guardTx) current(id domain.ChangeID) *draftState {
	if st := t.fresh[id]; st != nil && st.seq >= t.impactSeq[id] {
		return st
	}
	return nil
}

// readLog records the fold a transaction holding the lock of a change read the log for.
func (t *guardTx) readLog(st *draftState) {
	if !t.locked["change:"+string(st.change)] {
		return
	}
	if t.fresh == nil {
		t.fresh = map[domain.ChangeID]*draftState{}
	}
	t.fresh[st.change] = st
}

func (t *guardTx) keepDrafts(s *draftState) {
	if t.drafts == nil {
		t.drafts = map[domain.ChangeID]*draftState{}
	}
	t.drafts[s.change] = s
}

func (t *guardTx) needChange(id domain.ChangeID, what string) error {
	if id == "" {
		return fmt.Errorf("%s is written outside of a change (ADR 0049, 0054): %w", what, ErrInvalid)
	}
	if t.changeRefs == nil {
		t.changeRefs = map[domain.ChangeID]string{}
	}
	if _, ok := t.changeRefs[id]; !ok {
		t.changeRefs[id] = what
	}
	return nil
}

// change reads the header of a change (cached for the transaction).
func (t *guardTx) change(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	if c, ok := t.headers[id]; ok {
		return c, nil
	}
	c, err := t.Tx.Change(ctx, id)
	if err != nil {
		return c, err
	}
	if t.headers == nil {
		t.headers = map[domain.ChangeID]domain.Change{}
	}
	t.headers[id] = c
	return c, nil
}

// resolve returns the id of the node of a structure a change names by key; the node being written is its own
// reference (the bootstrap).
func (t *guardTx) resolve(ctx context.Context, kind, key string, self domain.Node) (domain.NodeID, error) {
	st := t.g.Structure(kind)
	if key == self.Key && st.Namespace == domain.NamespaceOf(self.Namespace) {
		return self.ID, nil
	}
	id, err := t.Tx.NodeIDByKey(ctx, st.Namespace, key)
	if errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("node %s: %s %q of its change does not exist: %w", self.Key, kind, key, ErrInvalid)
	}
	return id, err
}

func (t *guardTx) PutNode(ctx context.Context, n domain.Node) error {
	what := "node " + n.Key + " v" + fmt.Sprint(n.Version)
	if err := t.needChange(n.ChangeID, what); err != nil {
		return err
	}
	var prev *domain.Node
	if n.Version > 1 {
		// the previous version alone, whatever the length of the history (ADR 0140): the project is the node's
		// (the first parent when it names one: read once per transaction, versioncache.go)
		var vs []domain.Node
		if len(n.Parents) > 0 && n.Parents[0] >= 1 && n.Parents[0] < n.Version {
			p, err := t.Node(ctx, domain.NodeRef{ID: n.ID, Version: n.Parents[0]})
			if err != nil {
				return err
			}
			vs = []domain.Node{p}
		} else {
			var err error
			if vs, err = t.Tx.VersionRange(ctx, n.ID, 0, 1); err != nil {
				return err
			}
		}
		if len(vs) > 0 {
			p := vs[0]
			prev = &p
			if n.Project != "" && n.Project != p.Project {
				return fmt.Errorf("%s: the project of a node never changes (%s, not %s): %w", what, p.Project, n.Project, ErrInvalid)
			}
			n.Project = p.Project
		}
	}
	if n.Owner == "" && prev != nil {
		n.Owner = prev.Owner
	}
	if n.Owner == "" || n.Project == "" {
		c, err := t.change(ctx, n.ChangeID)
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%s: change %s does not exist: %w", what, n.ChangeID, ErrInvalid)
		}
		if err != nil {
			return err
		}
		if n.Owner == "" {
			if n.Owner, err = t.resolve(ctx, domain.StructureOrganisation, c.OwnerOrg, n); err != nil {
				return err
			}
		}
		if n.Project == "" {
			if n.Project, err = t.resolve(ctx, domain.StructureProject, c.ProjectID, n); err != nil {
				return err
			}
		}
	}
	if t.owners == nil {
		t.owners, t.projects = map[domain.NodeID]string{}, map[domain.NodeID]string{}
	}
	t.owners[n.Owner] = what
	t.projects[n.Project] = what
	if n.Landed >= 0 { // a negative order lands the version in no baseline (a test fixture)
		landed, err := t.landingOn(ctx, n.Branch)
		if err != nil {
			return err
		}
		n.Landed = landed
	}
	if err := t.Tx.PutNode(ctx, n); err != nil {
		return err
	}
	if t.written == nil {
		t.written = map[domain.NodeRef]bool{}
	}
	t.written[n.Ref()] = true
	return nil
}

func (t *guardTx) PutTag(ctx context.Context, tag domain.Tag) error {
	if err := t.needChange(tag.ChangeID, "tag "+tag.Name); err != nil {
		return err
	}
	return t.Tx.PutTag(ctx, tag)
}

func (t *guardTx) PutLink(ctx context.Context, l domain.Link) error {
	if err := t.needChange(l.ChangeID, "link "+l.Type); err != nil {
		return err
	}
	if !t.written[l.From] {
		return fmt.Errorf("link %s: version %s is not written by this transaction, a version never changes (ADR 0079): %w", l.Type, l.From, ErrConflict)
	}
	return t.Tx.PutLink(ctx, l)
}

func (t *guardTx) DropLink(ctx context.Context, from domain.NodeRef, row domain.LinkID, l domain.Link) error {
	if err := t.needChange(l.ChangeID, "link "+l.Type); err != nil {
		return err
	}
	if !t.written[from] {
		return fmt.Errorf("link %s: version %s is not written by this transaction, a version never changes (ADR 0079): %w", l.Type, from, ErrConflict)
	}
	return t.Tx.DropLink(ctx, from, row, l)
}

// PutLinkSet sets how a version written in the transaction holds its links, over versions of the same node written
// before it (ADR 0141).
func (t *guardTx) PutLinkSet(ctx context.Context, from domain.NodeRef, set LinkSet) error {
	if !t.written[from] {
		return fmt.Errorf("the links of version %s: it is not written by this transaction, a version never changes (ADR 0079): %w", from, ErrConflict)
	}
	for _, v := range []domain.Version{set.Of, set.Base} {
		if v >= from.Version {
			return fmt.Errorf("the links of version %s: v%d is not an earlier version of the node: %w", from, v, ErrInvalid)
		}
	}
	if set.Of != 0 && set.Base != 0 {
		return fmt.Errorf("the links of version %s: a version shares a set or writes a delta, not both: %w", from, ErrInvalid)
	}
	return t.Tx.PutLinkSet(ctx, from, set)
}

func (t *guardTx) SetNodeOrigin(ctx context.Context, ref domain.NodeRef, change domain.ChangeID, cn domain.ChangeImpactID, comment string) error {
	if err := t.needChange(change, "the origin of node "+ref.String()); err != nil {
		return err
	}
	delete(t.nodes, ref)
	return t.Tx.SetNodeOrigin(ctx, ref, change, cn, comment)
}

func (t *guardTx) JoinBranch(ctx context.Context, ref domain.NodeRef, branch string, change domain.ChangeID, _ int64) error {
	if err := t.needChange(change, "node "+ref.String()+" joining "+branch); err != nil {
		return err
	}
	landed, err := t.landingOn(ctx, branch)
	if err != nil {
		return err
	}
	return t.Tx.JoinBranch(ctx, ref, branch, change, landed)
}

func (t *guardTx) PutChange(ctx context.Context, c domain.Change) error {
	if c.OwnerOrg == "" || c.ProjectID == "" {
		return fmt.Errorf("change %s names no owner unit or no project (ADR 0054): %w", c.ID, ErrInvalid)
	}
	// a new sub-change is written with its parent's tree (it forks the parent's branch)
	if c.ParentID != "" {
		if err := t.LockChange(ctx, c.ParentID); err != nil {
			return err
		}
	}
	if err := t.LockChange(ctx, c.ID); err != nil {
		return err
	}
	if err := t.Tx.PutChange(ctx, c); err != nil {
		return err
	}
	if t.changes == nil {
		t.changes = map[domain.ChangeID]domain.Change{}
	}
	h := c
	h.Items, h.Nodes = nil, nil
	t.changes[c.ID] = h
	delete(t.headers, c.ID)
	delete(t.reads, c.ID)
	delete(t.heads, c.ID)
	return nil
}

func (t *guardTx) DeleteChange(ctx context.Context, id domain.ChangeID, namespace, branch string) error {
	// a version lands only in a baseline of the change that wrote or joined it (ADR 0140): the store refuses a change
	// that produced a baseline, and checks the entries stored
	if err := t.LockChange(ctx, id); err != nil {
		return err
	}
	t.purged, t.nodes = true, nil
	t.g.versions.reset()
	if err := t.Tx.DeleteChange(ctx, id, namespace, branch); err != nil {
		return err
	}
	delete(t.changes, id)
	delete(t.headers, id)
	delete(t.reads, id)
	delete(t.heads, id)
	return nil
}

// verify checks, at the end of the transaction, that what it wrote names changes, units and projects that exist, and
// that every version it landed is in a baseline.
func (t *guardTx) verify(ctx context.Context) error {
	for branch := range t.landing {
		return fmt.Errorf("versions landed on branch %s in no baseline: a version is written when its change lands (ADR 0079, 0140): %w", branch, ErrInvalid)
	}
	for id, what := range t.changeRefs {
		if _, ok := t.changes[id]; ok {
			continue
		}
		if _, err := t.change(ctx, id); errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%s names change %s, which does not exist: %w", what, id, ErrInvalid)
		} else if err != nil {
			return err
		}
	}
	for id, what := range t.owners {
		if err := t.inStructure(ctx, domain.StructureOrganisation, id, what+": owner"); err != nil {
			return err
		}
	}
	for id, what := range t.projects {
		if err := t.inStructure(ctx, domain.StructureProject, id, what+": project"); err != nil {
			return err
		}
	}
	for _, c := range t.changes {
		for _, ref := range []struct{ kind, key, what string }{
			{domain.StructureOrganisation, c.OwnerOrg, "owner"},
			{domain.StructureProject, c.ProjectID, "project"},
		} {
			st := t.g.Structure(ref.kind)
			id, err := t.Tx.NodeIDByKey(ctx, st.Namespace, ref.key)
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("change %s: %s %q is not a node of namespace %s: %w", c.ID, ref.what, ref.key, st.Namespace, ErrInvalid)
			}
			if err != nil {
				return err
			}
			if err := t.inStructure(ctx, ref.kind, id, "change "+string(c.ID)+": "+ref.what); err != nil {
				return err
			}
		}
	}
	return nil
}

// inStructure checks that a node exists (on any branch) and belongs to the structure of a kind.
func (t *guardTx) inStructure(ctx context.Context, kind string, id domain.NodeID, what string) error {
	st := t.g.Structure(kind)
	vs, err := t.Tx.VersionRange(ctx, id, 0, 1)
	if err == nil && len(vs) == 0 {
		return fmt.Errorf("%s %s does not exist: %w", what, id, ErrInvalid)
	}
	if err != nil {
		return err
	}
	if n := vs[0]; !t.g.inStructure(n, st) {
		return fmt.Errorf("%s %s is a %s of namespace %s, not a %s: %w", what, n.Key, n.Type, domain.NamespaceOf(n.Namespace), st.Type, ErrInvalid)
	}
	return nil
}
