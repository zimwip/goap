package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/outbox"
)

// Memory is an in-memory Repo. Transactions are serialized and rolled back by
// restoring a snapshot.
type Memory struct {
	mu sync.Mutex
	st memState
	// outbox holds the events of the committed transactions (ADR 0136)
	outbox outbox.Memory
}

type memState struct {
	versions  map[domain.NodeID][]domain.Node // index = version-1
	keys      map[string]domain.NodeID
	links     []domain.Link
	baselines map[domain.BaselineID]domain.Baseline
	changes   map[domain.ChangeID]domain.Change
	branches  map[string]domain.Branch
	// joins are the branches a version is part of besides the one it was written on (ADR 0032), with the change
	// whose merge made it join
	joins map[joinKey]memJoin
	// seq is the order of the last baseline allocated (ADR 0140)
	seq   int64
	nodes map[domain.ChangeID][]domain.ChangeImpact
	// log holds the entries of every change's log, in their order (ADR 0030)
	log []domain.LogEntry
	// tags name the state a change leaves (ADR 0056), in creation order
	tags []domain.Tag
}

// NewMemory returns an empty in-memory repository.
func NewMemory() *Memory {
	return &Memory{st: memState{
		versions:  map[domain.NodeID][]domain.Node{},
		keys:      map[string]domain.NodeID{},
		baselines: map[domain.BaselineID]domain.Baseline{},
		changes:   map[domain.ChangeID]domain.Change{},
		branches:  map[string]domain.Branch{},
		joins:     map[joinKey]memJoin{},
		nodes:     map[domain.ChangeID][]domain.ChangeImpact{},
	}}
}

func (s memState) clone() memState {
	c := memState{
		versions:  make(map[domain.NodeID][]domain.Node, len(s.versions)),
		keys:      maps.Clone(s.keys),
		links:     slices.Clone(s.links),
		baselines: maps.Clone(s.baselines),
		changes:   make(map[domain.ChangeID]domain.Change, len(s.changes)),
		branches:  maps.Clone(s.branches),
		joins:     maps.Clone(s.joins),
		log:       slices.Clone(s.log),
		tags:      slices.Clone(s.tags),
		seq:       s.seq,
		nodes:     make(map[domain.ChangeID][]domain.ChangeImpact, len(s.nodes)),
	}
	for k, v := range s.nodes {
		c.nodes[k] = slices.Clone(v)
	}
	for k, v := range s.versions {
		c.versions[k] = slices.Clone(v)
	}
	for k, v := range s.changes {
		c.changes[k] = v
	}
	return c
}

// InTx implements Repo.
func (m *Memory) InTx(ctx context.Context, fn func(tx Tx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := m.st.clone()
	tx := &memTx{st: &m.st}
	if err := fn(tx); err != nil {
		m.st = snapshot
		return err
	}
	m.outbox.Append(tx.outbox...)
	return nil
}

// Pending implements outbox.Store.
func (m *Memory) Pending(ctx context.Context, limit int) ([]outbox.Message, error) {
	return m.outbox.Pending(ctx, limit)
}

// MarkPublished implements outbox.Store.
func (m *Memory) MarkPublished(ctx context.Context, seqs []int64, at time.Time) error {
	return m.outbox.MarkPublished(ctx, seqs, at)
}

// PurgePublished implements outbox.Store.
func (m *Memory) PurgePublished(ctx context.Context, before time.Time) error {
	return m.outbox.PurgePublished(ctx, before)
}

type memTx struct {
	st *memState
	// outbox are the events of the transaction, stored when it commits
	outbox []outbox.Message
}

func (t *memTx) AppendOutbox(_ context.Context, msgs []outbox.Message) error {
	t.outbox = append(t.outbox, msgs...)
	return nil
}

type joinKey struct {
	ref    domain.NodeRef
	branch string
}

// memJoin is a version joining a branch: the change whose merge made it join, the order of the baseline it landed in.
type memJoin struct {
	change domain.ChangeID
	landed int64
}

// on reports whether a version is part of a branch: written there, or joined.
func (t *memTx) on(n domain.Node, branch string) bool {
	branch = domain.BranchOf(branch)
	return domain.BranchOf(n.Branch) == branch || t.st.joins[joinKey{n.Ref(), branch}].change != ""
}

func (t *memTx) Node(_ context.Context, ref domain.NodeRef) (domain.Node, error) {
	vs, ok := t.st.versions[ref.ID]
	if !ok || len(vs) == 0 {
		return domain.Node{}, fmt.Errorf("node %s: %w", ref.ID, ErrNotFound)
	}
	if ref.Version == 0 {
		return t.LatestOn(context.Background(), ref.ID, domain.MainBranch)
	}
	if int(ref.Version) > len(vs) || ref.Version < 1 {
		return domain.Node{}, fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	return vs[ref.Version-1], nil
}

func (t *memTx) LatestOn(_ context.Context, id domain.NodeID, branch string) (domain.Node, error) {
	vs := t.st.versions[id]
	for i := len(vs) - 1; i >= 0; i-- {
		if t.on(vs[i], branch) {
			return vs[i], nil
		}
	}
	return domain.Node{}, fmt.Errorf("node %s has no version on %s: %w", id, domain.BranchOf(branch), ErrNotFound)
}

func (t *memTx) Versions(_ context.Context, id domain.NodeID) ([]domain.Node, error) {
	vs, ok := t.st.versions[id]
	if !ok {
		return nil, fmt.Errorf("node %s: %w", id, ErrNotFound)
	}
	vs = slices.Clone(vs)
	for k := range t.st.joins {
		if k.ref.ID == id {
			vs[k.ref.Version-1].Joined = append(vs[k.ref.Version-1].Joined, k.branch)
		}
	}
	for i := range vs {
		slices.Sort(vs[i].Joined)
	}
	return vs, nil
}

func (t *memTx) VersionRange(ctx context.Context, id domain.NodeID, before domain.Version, limit int) ([]domain.Node, error) {
	vs, err := t.Versions(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if before > 0 && int(before) <= len(vs) {
		vs = vs[:before-1]
	}
	return vs[max(len(vs)-limit, 0):], err
}

func (t *memTx) DerivedNodes(_ context.Context, ref domain.NodeRef) ([]domain.Node, error) {
	var out []domain.Node
	for _, vs := range t.st.versions {
		for _, v := range vs {
			for _, o := range v.Origins {
				if o.ID == ref.ID && (ref.Version == 0 || o.Version == ref.Version) {
					out = append(out, v)
					break
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b domain.Node) int {
		if c := strings.Compare(a.Key, b.Key); c != 0 {
			return c
		}
		return int(a.Version) - int(b.Version)
	})
	return out, nil
}

func (t *memTx) Branch(_ context.Context, namespace, name string) (domain.Branch, error) {
	b, ok := t.st.branches[branchKey(namespace, name)]
	if !ok {
		return domain.Branch{}, fmt.Errorf("branch %s/%s: %w", domain.NamespaceOf(namespace), name, ErrNotFound)
	}
	return b, nil
}

func (t *memTx) Branches(_ context.Context, namespace string) ([]domain.Branch, error) {
	namespace = domain.NamespaceOf(namespace)
	out := make([]domain.Branch, 0, len(t.st.branches))
	for _, b := range t.st.branches {
		if b.Namespace == namespace {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// LockChange implements Tx: the transactions of the memory store are serialized.
func (t *memTx) LockChange(context.Context, domain.ChangeID) error { return nil }

// LockBranch implements Tx: the transactions of the memory store are serialized.
func (t *memTx) LockBranch(context.Context, string, string) error { return nil }

func (t *memTx) PutBranch(_ context.Context, b domain.Branch) error {
	b.Namespace = domain.NamespaceOf(b.Namespace)
	t.st.branches[branchKey(b.Namespace, b.Name)] = b
	return nil
}

// nsKey scopes a node key to its namespace.
func nsKey(namespace, key string) string { return domain.NamespaceOf(namespace) + "\x00" + key }

// branchKey scopes a branch name to its namespace.
func branchKey(namespace, name string) string {
	return domain.NamespaceOf(namespace) + "\x00" + domain.BranchOf(name)
}

func (t *memTx) NodeIDByKey(_ context.Context, namespace, key string) (domain.NodeID, error) {
	id, ok := t.st.keys[nsKey(namespace, key)]
	if !ok {
		return "", fmt.Errorf("node key %q: %w", key, ErrNotFound)
	}
	return id, nil
}

func (t *memTx) NodeByKey(ctx context.Context, namespace, key string) (domain.Node, error) {
	id, ok := t.st.keys[nsKey(namespace, key)]
	if !ok {
		return domain.Node{}, fmt.Errorf("node key %q: %w", key, ErrNotFound)
	}
	return t.Node(ctx, domain.NodeRef{ID: id})
}

func (t *memTx) NodesIn(ctx context.Context, baseline domain.BaselineID, nodeType string) ([]domain.Node, error) {
	return storedNodesIn(ctx, t, baseline, nodeType)
}

func (t *memTx) LatestNodes(ctx context.Context, namespace, branch string) ([]domain.Node, error) {
	namespace = domain.NamespaceOf(namespace)
	out := make([]domain.Node, 0, len(t.st.versions))
	for id := range t.st.versions {
		n, err := t.LatestOn(ctx, id, branch)
		if err != nil {
			continue
		}
		if n.Namespace == namespace {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (t *memTx) Namespaces(_ context.Context) ([]string, error) {
	seen := map[string]bool{}
	for _, vs := range t.st.versions {
		if len(vs) > 0 {
			seen[vs[0].Namespace] = true
		}
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out, nil
}

func (t *memTx) OutLinks(_ context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	var out []domain.Link
	for _, l := range t.st.links {
		if l.From == ref {
			out = append(out, l)
		}
	}
	return out, nil
}

func (t *memTx) InLinks(_ context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	var out []domain.Link
	for _, l := range t.st.links {
		if l.To == ref {
			out = append(out, l)
		}
	}
	return out, nil
}

func (t *memTx) Baseline(ctx context.Context, id domain.BaselineID) (domain.Baseline, error) {
	b, ok := t.st.baselines[id]
	if !ok {
		return domain.Baseline{}, fmt.Errorf("baseline %s: %w", id, ErrNotFound)
	}
	var err error
	b.Nodes, err = storedNodes(ctx, t, b)
	return b, err
}

func (t *memTx) Baselines(ctx context.Context, namespace string) ([]domain.Baseline, error) {
	namespace = domain.NamespaceOf(namespace)
	out := make([]domain.Baseline, 0, len(t.st.baselines))
	for _, b := range t.st.baselines {
		if b.Namespace != namespace {
			continue
		}
		var err error
		if b.Nodes, err = storedNodes(ctx, t, b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (t *memTx) Change(_ context.Context, id domain.ChangeID) (domain.Change, error) {
	c, ok := t.st.changes[id]
	if !ok {
		return domain.Change{}, fmt.Errorf("change %s: %w", id, ErrNotFound)
	}
	facts, _ := t.Log(context.Background(), factsFilter(id))
	items, err := itemsOf(facts)
	if err != nil {
		return c, err
	}
	c.Items = items
	c.Nodes = slices.Clone(t.st.nodes[id])
	return c, nil
}

// ChangeHead implements Tx.
func (t *memTx) ChangeHead(_ context.Context, id domain.ChangeID) (domain.Change, error) {
	c, ok := t.st.changes[id]
	if !ok {
		return domain.Change{}, fmt.Errorf("change %s: %w", id, ErrNotFound)
	}
	c.Items, c.Nodes = nil, nil
	return c, nil
}

// ChangeHeads implements Tx.
func (t *memTx) ChangeHeads(ctx context.Context) ([]domain.Change, error) {
	out := make([]domain.Change, 0, len(t.st.changes))
	for id := range t.st.changes {
		c, _ := t.ChangeHead(ctx, id)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (t *memTx) Changes(ctx context.Context) ([]domain.Change, error) {
	out := make([]domain.Change, 0, len(t.st.changes))
	for id := range t.st.changes {
		c, _ := t.Change(ctx, id)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (t *memTx) SubChanges(ctx context.Context, parent domain.ChangeID) ([]domain.Change, error) {
	var out []domain.Change
	for id, c := range t.st.changes {
		if c.ParentID == parent {
			c, _ := t.Change(ctx, id)
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (t *memTx) PutNode(_ context.Context, n domain.Node) error {
	vs := t.st.versions[n.ID]
	if int(n.Version) != len(vs)+1 {
		return fmt.Errorf("node %s: expected version %d: %w", n.Ref(), len(vs)+1, ErrConflict)
	}
	if n.Version == 1 {
		if _, dup := t.st.keys[nsKey(n.Namespace, n.Key)]; dup && n.Key != "" {
			return fmt.Errorf("node key %q already used: %w", n.Key, ErrConflict)
		}
		if n.Key != "" {
			t.st.keys[nsKey(n.Namespace, n.Key)] = n.ID
		}
	}
	if n.Version > 1 {
		n.Project = vs[0].Project // the project is the node's, set by its first version
	}
	t.st.versions[n.ID] = append(vs, n)
	return nil
}

func (t *memTx) linkAt(id domain.LinkID) (int, error) {
	for i, l := range t.st.links {
		if l.ID == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("link %s: %w", id, ErrNotFound)
}

func (t *memTx) Link(_ context.Context, id domain.LinkID) (domain.Link, error) {
	i, err := t.linkAt(id)
	if err != nil {
		return domain.Link{}, err
	}
	return t.st.links[i], nil
}

func (t *memTx) PutLink(_ context.Context, l domain.Link) error {
	t.st.links = append(t.st.links, l)
	return nil
}

func (t *memTx) PutBaseline(_ context.Context, b domain.Baseline) error {
	if _, dup := t.st.baselines[b.ID]; dup {
		return fmt.Errorf("baseline %s: %w", b.ID, ErrConflict)
	}
	b.Namespace = domain.NamespaceOf(b.Namespace)
	for id, v := range b.Nodes {
		vs := t.st.versions[id]
		if v < 0 {
			v = -v
		}
		if len(vs) < int(v) {
			return fmt.Errorf("baseline %s: node %s v%d: %w", b.ID, id, v, ErrNotFound)
		}
		if vs[0].Namespace != b.Namespace {
			return fmt.Errorf("baseline %s is of namespace %s, node %s is of namespace %s: %w",
				b.ID, b.Namespace, id, vs[0].Namespace, ErrInvalid)
		}
	}
	b.Nodes = maps.Clone(b.Nodes)
	if b.Gap > 0 {
		b.Nodes = nil // only the header is kept: the graph computes the state (ADR 0140)
	}
	t.st.baselines[b.ID] = b
	return nil
}

func (t *memTx) BaselineHeader(_ context.Context, id domain.BaselineID) (domain.Baseline, error) {
	b, ok := t.st.baselines[id]
	if !ok {
		return domain.Baseline{}, fmt.Errorf("baseline %s: %w", id, ErrNotFound)
	}
	b.Nodes = nil
	return b, nil
}

func (t *memTx) StoredVersions(_ context.Context, id domain.BaselineID, ids []domain.NodeID) (map[domain.NodeID]domain.Version, domain.BaselineID, error) {
	out := map[domain.NodeID]domain.Version{}
	want := func(n domain.NodeID) bool {
		_, done := out[n]
		return !done && (ids == nil || slices.Contains(ids, n))
	}
	for cur := id; ; {
		b, ok := t.st.baselines[cur]
		if !ok {
			return nil, "", fmt.Errorf("baseline %s: %w", cur, ErrNotFound)
		}
		if b.Gap > 0 {
			return out, cur, nil
		}
		for n, v := range b.Nodes {
			if want(n) {
				out[n] = max(v, 0)
			}
		}
		if b.BaseID == "" {
			return out, "", nil
		}
		cur = b.BaseID
	}
}

func (t *memTx) LandedVersions(_ context.Context, namespace, branch string, after, upTo int64, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	namespace, branch = domain.NamespaceOf(namespace), domain.BranchOf(branch)
	type landing struct {
		landed int64
		v      domain.Node
	}
	last := map[domain.NodeID]landing{}
	put := func(landed int64, v domain.Node) {
		if landed <= after || landed > upTo || domain.NamespaceOf(v.Namespace) != namespace || ids != nil && !slices.Contains(ids, v.ID) {
			return
		}
		if l, ok := last[v.ID]; !ok || landed > l.landed || landed == l.landed && v.Version > l.v.Version {
			last[v.ID] = landing{landed, v}
		}
	}
	for _, vs := range t.st.versions {
		for _, v := range vs {
			if domain.BranchOf(v.Branch) == branch {
				put(v.Landed, v)
			}
		}
	}
	for k, j := range t.st.joins {
		if k.branch == branch {
			put(j.landed, t.st.versions[k.ref.ID][k.ref.Version-1])
		}
	}
	out := make(map[domain.NodeID]domain.Version, len(last))
	for id, l := range last {
		if l.v.Deleted {
			out[id] = 0
		} else {
			out[id] = l.v.Version
		}
	}
	return out, nil
}

func (t *memTx) LatestStored(_ context.Context, namespace, branch string, after, upTo int64) (domain.BaselineID, error) {
	var best domain.Baseline
	for _, b := range t.st.baselines {
		if b.Gap == 0 && b.Namespace == domain.NamespaceOf(namespace) && domain.BranchOf(b.Branch) == domain.BranchOf(branch) && b.Seq > after && b.Seq <= upTo && b.Seq > best.Seq {
			best = b
		}
	}
	return best.ID, nil
}

func (t *memTx) NextLanding(context.Context) (int64, error) {
	t.st.seq++
	return t.st.seq, nil
}

func (t *memTx) NodesAt(ctx context.Context, refs []domain.NodeRef, nodeType string) ([]domain.Node, error) {
	var out []domain.Node
	for _, r := range refs {
		n, err := t.Node(ctx, r)
		if err != nil {
			return nil, err
		}
		if nodeType == "" || n.Type == nodeType {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (t *memTx) BranchNodeIDs(_ context.Context, branch string) ([]domain.NodeID, error) {
	branch = domain.BranchOf(branch)
	seen := map[domain.NodeID]bool{}
	var out []domain.NodeID
	add := func(id domain.NodeID) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for id, vs := range t.st.versions {
		for _, v := range vs {
			if domain.BranchOf(v.Branch) == branch {
				add(id)
				break
			}
		}
	}
	for k := range t.st.joins {
		if k.branch == branch {
			add(k.ref.ID)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (t *memTx) BaselineHeaders(_ context.Context, namespace string) ([]domain.Baseline, error) {
	namespace = domain.NamespaceOf(namespace)
	var out []domain.Baseline
	for _, b := range t.st.baselines {
		if b.Namespace == namespace {
			b.Nodes = nil
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (t *memTx) BranchBaselineHeaders(ctx context.Context, namespace, branch string) ([]domain.Baseline, error) {
	all, err := t.BaselineHeaders(ctx, namespace)
	return slices.DeleteFunc(all, func(b domain.Baseline) bool { return domain.BranchOf(b.Branch) != domain.BranchOf(branch) }), err
}

func (t *memTx) VersionJoins(_ context.Context, id domain.NodeID) ([]domain.VersionJoin, error) {
	var out []domain.VersionJoin
	for k, j := range t.st.joins {
		if k.ref.ID == id {
			out = append(out, domain.VersionJoin{Version: k.ref.Version, Branch: k.branch, Change: j.change})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Version < out[j].Version || out[i].Version == out[j].Version && out[i].Branch < out[j].Branch
	})
	return out, nil
}

func (t *memTx) MaterializeBaseline(_ context.Context, id domain.BaselineID, nodes map[domain.NodeID]domain.Version) error {
	b, ok := t.st.baselines[id]
	if !ok {
		return fmt.Errorf("baseline %s: %w", id, ErrNotFound)
	}
	b.Nodes, b.Gap, b.Depth, b.BaseID = maps.Clone(nodes), 0, 0, ""
	t.st.baselines[id] = b
	return nil
}

func (t *memTx) PutChange(_ context.Context, c domain.Change) error {
	c.Items, c.Nodes = nil, nil // the facts are in the log
	c.DependsOn = slices.Clone(c.DependsOn)
	t.st.changes[c.ID] = c
	return nil
}

func (t *memTx) AppendLog(_ context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	if _, ok := t.st.changes[e.Change]; !ok { // a reference to an unknown change, as the SQL foreign key
		return e, fmt.Errorf("change %s: %w", e.Change, ErrInvalid)
	}
	for _, x := range t.st.log {
		if x.ID == e.ID {
			return e, fmt.Errorf("log entry %s: %w", e.ID, ErrConflict)
		}
	}
	e.Payload = slices.Clone(e.Payload)
	e.Seq = int64(len(t.st.log) + 1)
	t.st.log = append(t.st.log, e)
	return e, nil
}

func (t *memTx) LogCounts(_ context.Context, f domain.LogFilter) (map[string]int, error) {
	f.AfterSeq = 0
	out := map[string]int{}
	for _, e := range t.st.log {
		if f.Match(e) {
			out[e.Type]++
		}
	}
	return out, nil
}

func (t *memTx) Log(_ context.Context, f domain.LogFilter) ([]domain.LogEntry, error) {
	var out []domain.LogEntry
	for _, e := range t.st.log {
		if f.Match(e) {
			out = append(out, e)
			if f.Limit > 0 && len(out) == f.Limit {
				break
			}
		}
	}
	return out, nil
}

func (t *memTx) OpenChangeIDs(_ context.Context) ([]domain.ChangeID, error) {
	var out []domain.ChangeID
	for id, c := range t.st.changes {
		if c.Status != domain.ChangeApplied && c.Status != domain.ChangeAbandoned {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return t.st.changes[out[i]].CreatedAt.Before(t.st.changes[out[j]].CreatedAt) })
	return out, nil
}

func (t *memTx) DeleteChangeImpact(_ context.Context, change domain.ChangeID, id domain.ChangeImpactID) error {
	list := t.st.nodes[change]
	i := slices.IndexFunc(list, func(cn domain.ChangeImpact) bool { return cn.ID == id })
	if i < 0 {
		return fmt.Errorf("change impact %s: %w", id, ErrNotFound)
	}
	t.st.nodes[change] = slices.Delete(slices.Clone(list), i, i+1)
	return nil
}

func (t *memTx) PutChangeImpact(_ context.Context, change domain.ChangeID, cn domain.ChangeImpact) error {
	if _, ok := t.st.changes[change]; !ok {
		return fmt.Errorf("change %s: %w", change, ErrNotFound)
	}
	list := t.st.nodes[change]
	for i := range list {
		if list[i].ID == cn.ID {
			list[i] = cn
			return nil
		}
		if cn.Pre != nil && list[i].Pre != nil && list[i].Pre.ID == cn.Pre.ID && list[i].Flow == cn.Flow && !list[i].Superseded && !cn.Superseded {
			return fmt.Errorf("change impact %s: node already in the change: %w", cn.Key, ErrConflict)
		}
	}
	t.st.nodes[change] = append(list, cn)
	return nil
}

func (t *memTx) ChangeImpacts(_ context.Context, change domain.ChangeID) ([]domain.ChangeImpact, error) {
	return slices.Clone(t.st.nodes[change]), nil
}

// nodeChanges lists the changes holding a change impact on a node, oldest first.
func (t *memTx) nodeChanges(node domain.NodeID) []domain.ChangeID {
	var out []domain.ChangeID
	for id, list := range t.st.nodes {
		for _, cn := range list {
			if (cn.Pre != nil && cn.Pre.ID == node) || (cn.Post != nil && cn.Post.ID == node) {
				out = append(out, id)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return t.st.changes[out[i]].CreatedAt.Before(t.st.changes[out[j]].CreatedAt) })
	return out
}

// NodeChangePage implements Tx: the cursor of a change is its rank among the node's, from 1.
func (t *memTx) NodeChangePage(_ context.Context, node domain.NodeID, before int64, limit int) ([]NodeChange, error) {
	all := t.nodeChanges(node)
	if before > 0 && int(before) <= len(all) {
		all = all[:before-1]
	}
	var out []NodeChange
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, NodeChange{Change: all[i], Seq: int64(i + 1)})
	}
	return out, nil
}

func (t *memTx) SetNodeOrigin(_ context.Context, ref domain.NodeRef, change domain.ChangeID, cn domain.ChangeImpactID, comment string) error {
	vs := t.st.versions[ref.ID]
	if ref.Version < 1 || int(ref.Version) > len(vs) {
		return fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	vs[ref.Version-1].ChangeID, vs[ref.Version-1].ChangeImpact, vs[ref.Version-1].Comment = change, cn, comment
	return nil
}

func (t *memTx) JoinBranch(_ context.Context, ref domain.NodeRef, branch string, change domain.ChangeID, landed int64) error {
	vs := t.st.versions[ref.ID]
	if ref.Version < 1 || int(ref.Version) > len(vs) {
		return fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	if branch = domain.BranchOf(branch); domain.BranchOf(vs[ref.Version-1].Branch) != branch {
		if _, ok := t.st.joins[joinKey{ref, branch}]; !ok {
			t.st.joins[joinKey{ref, branch}] = memJoin{change: change, landed: landed}
		}
	}
	return nil
}

func (t *memTx) PutTag(_ context.Context, tag domain.Tag) error {
	if _, ok := t.st.changes[tag.ChangeID]; !ok {
		return fmt.Errorf("tag %q: change %s: %w", tag.Name, tag.ChangeID, ErrNotFound)
	}
	if tag.BaselineID != "" {
		if _, ok := t.st.baselines[tag.BaselineID]; !ok {
			return fmt.Errorf("tag %q: baseline %s: %w", tag.Name, tag.BaselineID, ErrNotFound)
		}
	}
	for i, old := range t.st.tags {
		if old.ID == tag.ID {
			t.st.tags[i] = tag
			return nil
		}
	}
	t.st.tags = append(t.st.tags, tag)
	return nil
}

func (t *memTx) DeleteTag(_ context.Context, id domain.TagID) error {
	for i, old := range t.st.tags {
		if old.ID == id {
			t.st.tags = slices.Delete(t.st.tags, i, i+1)
			return nil
		}
	}
	return fmt.Errorf("tag %s: %w", id, ErrNotFound)
}

func (t *memTx) Tags(_ context.Context, f domain.TagFilter) ([]domain.Tag, error) {
	var out []domain.Tag
	for _, tag := range t.st.tags {
		if (f.Namespace == "" || tag.Namespace == domain.NamespaceOf(f.Namespace)) && (f.Name == "" || tag.Name == f.Name) && (f.Change == "" || tag.ChangeID == f.Change) {
			out = append(out, tag)
		}
	}
	return out, nil
}

func (t *memTx) DeleteChange(_ context.Context, id domain.ChangeID, namespace, branch string) error {
	used := fmt.Errorf("change %s: what it wrote is used by the graph: %w", id, ErrConflict)
	for _, b := range t.st.baselines {
		if b.ChangeID == id {
			return used
		}
	}
	// what the change wrote must be the tail of its nodes: nothing was built on it
	cut := map[domain.NodeID]int{} // first version to remove
	for nid, vs := range t.st.versions {
		for i, v := range vs {
			if v.ChangeID != id {
				continue
			}
			if _, ok := cut[nid]; !ok {
				cut[nid] = i
			}
			for _, b := range t.st.baselines {
				if b.Contains(v.Ref()) {
					return used
				}
			}
		}
		if i, ok := cut[nid]; ok {
			for _, v := range vs[i:] {
				if v.ChangeID != id {
					return used
				}
			}
		}
	}
	removed := func(r domain.NodeRef) bool {
		i, ok := cut[r.ID]
		return ok && int(r.Version) > i
	}
	var kept []domain.Link
	for _, l := range t.st.links {
		switch {
		case l.ChangeID == id, removed(l.From):
		case removed(l.To):
			return used
		default:
			kept = append(kept, l)
		}
	}
	t.st.links = kept
	for nid, i := range cut {
		for _, v := range t.st.versions[nid][i:] {
			delete(t.st.joins, joinKey{v.Ref(), domain.BranchOf(v.Branch)})
		}
		if i == 0 {
			for k, kid := range t.st.keys {
				if kid == nid {
					delete(t.st.keys, k)
				}
			}
			delete(t.st.versions, nid)
		} else {
			t.st.versions[nid] = t.st.versions[nid][:i]
		}
	}
	for k, j := range t.st.joins {
		if removed(k.ref) || j.change == id {
			delete(t.st.joins, k)
		}
	}
	t.st.log = slices.DeleteFunc(t.st.log, func(e domain.LogEntry) bool { return e.Change == id })
	delete(t.st.nodes, id)
	t.st.tags = slices.DeleteFunc(t.st.tags, func(tag domain.Tag) bool { return tag.ChangeID == id })
	if branch != "" {
		delete(t.st.branches, branchKey(namespace, branch))
	}
	if _, ok := t.st.changes[id]; !ok {
		return fmt.Errorf("change %s: %w", id, ErrNotFound)
	}
	delete(t.st.changes, id)
	return nil
}

func (t *memTx) LinksFrom(_ context.Context, refs []domain.NodeRef) ([]domain.Link, error) {
	from := map[domain.NodeRef]bool{}
	for _, r := range refs {
		from[r] = true
	}
	var out []domain.Link
	for _, l := range t.st.links {
		if from[l.From] {
			out = append(out, l)
		}
	}
	slices.SortFunc(out, func(a, b domain.Link) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return out, nil
}

func (t *memTx) LinksTo(_ context.Context, id domain.NodeID) ([]domain.Link, error) {
	var out []domain.Link
	for _, l := range t.st.links {
		if l.To.ID == id {
			out = append(out, l)
		}
	}
	slices.SortFunc(out, func(a, b domain.Link) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return out, nil
}

func (t *memTx) NodePage(ctx context.Context, refs []domain.NodeRef, q NodeQuery) (NodePage, error) {
	nodes, err := t.NodesAt(ctx, refs, "")
	if err != nil {
		return NodePage{}, err
	}
	slices.SortStableFunc(nodes, func(a, b domain.Node) int { return strings.Compare(a.Key, b.Key) })
	return pageNodes(nodes, q), nil
}

func (t *memTx) LinkPage(ctx context.Context, refs []domain.NodeRef, q LinkQuery) (LinkPage, error) {
	in := map[domain.NodeRef]bool{}
	for _, r := range refs {
		in[r] = true
	}
	out, err := t.LinksFrom(ctx, refs)
	if err != nil {
		return LinkPage{}, err
	}
	links := out[:0]
	for _, l := range out {
		if in[l.To] {
			links = append(links, l)
		}
	}
	return pageLinks(links, q), nil
}
