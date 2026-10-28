package graph

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
)

// Memory is an in-memory Repo. The transactions of a read-only context (platform.ReadOnly) run together, the others
// one at a time, rolled back by restoring the snapshot their first write took.
type Memory struct {
	mu sync.RWMutex
	st memState
	// outbox holds the events of the committed transactions (ADR 0136)
	outbox outbox.Memory
}

type memState struct {
	versions map[domain.NodeID][]domain.Node // index = version-1
	keys     map[string]domain.NodeID
	links    []domain.Link
	// drops are the rows of a delta taking a link out of the set it is a delta over, sets how a version holds its
	// links when not its own whole set (ADR 0141)
	drops     []memDrop
	sets      map[domain.NodeRef]LinkSet
	baselines map[domain.BaselineID]domain.Baseline
	changes   map[domain.ChangeID]domain.Change
	branches  map[string]domain.Branch
	// joins are the branches a version is part of besides the one it was written on (ADR 0032), with the change
	// whose merge made it join
	joins map[joinKey]memJoin
	// seq is the order of the last baseline allocated (ADR 0140)
	seq int64
	// impacts are the change impacts of the versions each change wrote (ADR 0174), in the order written
	impacts map[domain.ChangeID][]CommitImpact
	// log holds the entries of every change's log, in their order (ADR 0030)
	log []domain.LogEntry
	// tags name node versions (ADR 0143 §6), in creation order
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
		impacts:   map[domain.ChangeID][]CommitImpact{},
		sets:      map[domain.NodeRef]LinkSet{},
	}}
}

// memDrop is a row of a delta taking out link removes (ADR 0141).
type memDrop struct {
	row     domain.LinkID
	from    domain.NodeRef
	removes domain.LinkID
	to      domain.NodeRef
	change  domain.ChangeID
}

func (s memState) clone() memState {
	c := memState{
		versions:  make(map[domain.NodeID][]domain.Node, len(s.versions)),
		keys:      maps.Clone(s.keys),
		links:     slices.Clone(s.links),
		drops:     slices.Clone(s.drops),
		sets:      maps.Clone(s.sets),
		baselines: maps.Clone(s.baselines),
		changes:   make(map[domain.ChangeID]domain.Change, len(s.changes)),
		branches:  maps.Clone(s.branches),
		joins:     maps.Clone(s.joins),
		log:       slices.Clone(s.log),
		tags:      slices.Clone(s.tags),
		seq:       s.seq,
		impacts:   make(map[domain.ChangeID][]CommitImpact, len(s.impacts)),
	}
	for k, v := range s.impacts {
		c.impacts[k] = slices.Clone(v)
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
	if platform.IsReadOnly(ctx) {
		if wrote, err := m.readTx(fn); !wrote {
			return err
		}
		slog.WarnContext(ctx, "graph: a read-only request writes; running its transaction on the writer")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{st: &m.st}
	if err := fn(tx); err != nil {
		if tx.snap != nil {
			m.st = *tx.snap
		}
		return err
	}
	m.outbox.Append(tx.outbox...)
	return nil
}

// readTx runs fn beside the other readers; wrote says it tried to write, so it must run again on the writer.
func (m *Memory) readTx(fn func(tx Tx) error) (wrote bool, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tx := &memTx{st: &m.st, readOnly: true}
	err = fn(tx)
	return tx.wrote, err
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
	// snap is the state before the first write of the transaction, restored when it fails: taken at that write, so a
	// transaction that only reads copies nothing
	snap *memState
	// readOnly is a transaction of a read-only context, run beside the other readers; wrote says it tried to write
	readOnly, wrote bool
	// outbox are the events of the transaction, stored when it commits
	outbox []outbox.Message
}

// write takes the snapshot of the state before the first write of the transaction; a read-only one refuses it.
func (t *memTx) write() error {
	if t.readOnly {
		t.wrote = true
		return errWrites
	}
	if t.snap == nil {
		s := t.st.clone()
		t.snap = &s
	}
	return nil
}

func (t *memTx) AppendOutbox(_ context.Context, msgs []outbox.Message) error {
	if err := t.write(); err != nil {
		return err
	}
	t.outbox = append(t.outbox, msgs...)
	return nil
}

type joinKey struct {
	ref    domain.NodeRef
	branch string
}

// memJoin is a version joining a branch: the change whose merge made it join, the order of the baseline it committed in.
type memJoin struct {
	change    domain.ChangeID
	committed int64
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
			vs[k.ref.Version-1].Joined = append(slices.Clip(vs[k.ref.Version-1].Joined), k.branch) // never into the stored array
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
	if err := t.write(); err != nil {
		return err
	}
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

func (t *memTx) NodeIDsByKey(_ context.Context, namespace string, keys []string) (map[string]domain.NodeID, error) {
	out := make(map[string]domain.NodeID, len(keys))
	for _, k := range keys {
		if id, ok := t.st.keys[nsKey(namespace, k)]; ok {
			out[k] = id
		}
	}
	return out, nil
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
	return t.held(ref), nil
}

// setOf is how a version holds its links (ADR 0141).
func (t *memTx) setOf(ref domain.NodeRef) LinkSet {
	if s, ok := t.st.sets[ref]; ok {
		return s
	}
	return LinkSet{Of: ref.Version}
}

// held is the links a version holds, read as leaving it: the rows of its set and of the sets it is a delta over, minus
// those a row of the chain drops (ADR 0141).
func (t *memTx) held(ref domain.NodeRef) []domain.Link {
	chain := map[domain.Version]bool{}
	for v := t.setOf(ref).Of; v != 0 && !chain[v]; v = t.setOf(domain.NodeRef{ID: ref.ID, Version: v}).Base {
		chain[v] = true
	}
	dropped := map[domain.LinkID]bool{}
	for _, d := range t.st.drops {
		if d.from.ID == ref.ID && chain[d.from.Version] {
			dropped[d.removes] = true
		}
	}
	var out []domain.Link
	for _, l := range t.st.links {
		if l.From.ID == ref.ID && chain[l.From.Version] && !dropped[l.ID] {
			l.From = ref
			out = append(out, l)
		}
	}
	slices.SortFunc(out, func(a, b domain.Link) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return out
}

// entering is the links entering the versions of node id that keep (all when nil), one per version holding it, by id
// then version.
func (t *memTx) entering(id domain.NodeID, keep func(domain.NodeRef) bool) []domain.Link {
	sources := map[domain.NodeID]bool{}
	for _, l := range t.st.links {
		if l.To.ID == id {
			sources[l.From.ID] = true
		}
	}
	var out []domain.Link
	for src := range sources {
		for _, v := range t.st.versions[src] {
			for _, l := range t.held(v.Ref()) {
				if l.To.ID == id && (keep == nil || keep(l.To)) {
					out = append(out, l)
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b domain.Link) int {
		if c := strings.Compare(string(a.ID), string(b.ID)); c != 0 {
			return c
		}
		return int(a.From.Version) - int(b.From.Version)
	})
	return out
}

func (t *memTx) LinksInto(ctx context.Context, refs []domain.NodeRef) ([]domain.Link, error) {
	var out []domain.Link
	for _, r := range refs {
		ls, _ := t.InLinks(ctx, r)
		out = append(out, ls...)
	}
	slices.SortStableFunc(out, func(a, b domain.Link) int {
		if c := strings.Compare(string(a.ID), string(b.ID)); c != 0 {
			return c
		}
		return int(a.From.Version) - int(b.From.Version)
	})
	return out, nil
}

func (t *memTx) LatestVersionsOn(ctx context.Context, ids []domain.NodeID, branch string) (map[domain.NodeID]domain.Version, error) {
	out := make(map[domain.NodeID]domain.Version, len(ids))
	for _, id := range ids {
		if n, err := t.LatestOn(ctx, id, branch); err == nil {
			out[id] = n.Version
		}
	}
	return out, nil
}

func (t *memTx) LatestVersions(_ context.Context, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	out := make(map[domain.NodeID]domain.Version, len(ids))
	for _, id := range ids {
		if vs := t.st.versions[id]; len(vs) > 0 {
			out[id] = vs[len(vs)-1].Version
		}
	}
	return out, nil
}

func (t *memTx) InLinks(_ context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	return t.entering(ref.ID, func(to domain.NodeRef) bool { return to == ref }), nil
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
func (t *memTx) ChangeHeads(ctx context.Context, f ChangesFilter) ([]domain.Change, error) {
	f.Query = ""
	var out []domain.Change
	for id := range t.st.changes {
		if c, _ := t.ChangeHead(ctx, id); f.Match(c) {
			out = append(out, c)
		}
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
	if err := t.write(); err != nil {
		return err
	}
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
		n.Creator = vs[0].Creator
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

func (t *memTx) PutLinks(_ context.Context, ls []domain.Link) error {
	if err := t.write(); err != nil {
		return err
	}
	t.st.links = append(t.st.links, ls...)
	return nil
}

func (t *memTx) DropLink(_ context.Context, from domain.NodeRef, row domain.LinkID, l domain.Link) error {
	if err := t.write(); err != nil {
		return err
	}
	t.st.drops = append(t.st.drops, memDrop{row: row, from: from, removes: l.ID, to: l.To, change: l.ChangeID})
	return nil
}

func (t *memTx) PutLinkSet(_ context.Context, from domain.NodeRef, set LinkSet) error {
	if err := t.write(); err != nil {
		return err
	}
	if set.Of == 0 {
		set.Of = from.Version
	}
	t.st.sets[from] = set
	return nil
}

func (t *memTx) LinkSetOf(_ context.Context, ref domain.NodeRef) (LinkSet, error) {
	if vs := t.st.versions[ref.ID]; ref.Version < 1 || int(ref.Version) > len(vs) {
		return LinkSet{}, fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	return t.setOf(ref), nil
}

func (t *memTx) LinkSetsOf(ctx context.Context, refs []domain.NodeRef) (map[domain.NodeRef]LinkSet, error) {
	out := make(map[domain.NodeRef]LinkSet, len(refs))
	for _, r := range refs {
		if s, err := t.LinkSetOf(ctx, r); err == nil {
			out[r] = s
		}
	}
	return out, nil
}

func (t *memTx) PutBaseline(_ context.Context, b domain.Baseline) error {
	if err := t.write(); err != nil {
		return err
	}
	if _, dup := t.st.baselines[b.ID]; dup {
		return fmt.Errorf("baseline %s: %w", b.ID, ErrConflict)
	}
	b.Namespace = domain.NamespaceOf(b.Namespace)
	b.Nodes = nil // only the header is kept: the state is the selection of the versions committed (ADR 0143)
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

func (t *memTx) CommittedVersions(ctx context.Context, namespace, branch string, after, upTo int64, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	ls, err := t.commits(namespace, branch, after, upTo, ids)
	return committedVersions(ls), err
}

func (t *memTx) BranchCommits(_ context.Context, namespace, branch string, upTo int64, ids []domain.NodeID) (map[domain.NodeID]CommittedVersion, error) {
	return t.commits(namespace, branch, 0, upTo, ids)
}

func (t *memTx) commits(namespace, branch string, after, upTo int64, ids []domain.NodeID) (map[domain.NodeID]CommittedVersion, error) {
	namespace, branch = domain.NamespaceOf(namespace), domain.BranchOf(branch)
	type commit struct {
		committed int64
		v         domain.Node
	}
	last := map[domain.NodeID]commit{}
	var want map[domain.NodeID]bool
	if ids != nil {
		want = make(map[domain.NodeID]bool, len(ids))
		for _, id := range ids {
			want[id] = true
		}
	}
	put := func(committed int64, v domain.Node) {
		if committed <= after || committed > upTo || domain.NamespaceOf(v.Namespace) != namespace || want != nil && !want[v.ID] {
			return
		}
		if l, ok := last[v.ID]; !ok || committed > l.committed || committed == l.committed && v.Version > l.v.Version {
			last[v.ID] = commit{committed, v}
		}
	}
	for _, vs := range t.st.versions {
		for _, v := range vs {
			if domain.BranchOf(v.Branch) == branch {
				put(v.Committed, v)
			}
		}
	}
	for k, j := range t.st.joins {
		if k.branch == branch {
			put(j.committed, t.st.versions[k.ref.ID][k.ref.Version-1])
		}
	}
	out := make(map[domain.NodeID]CommittedVersion, len(last))
	for id, l := range last {
		out[id] = CommittedVersion{Version: l.v.Version, Deleted: l.v.Deleted, Revert: l.v.Reason == domain.ReasonRevert}
	}
	return out, nil
}

func (t *memTx) NextCommit(context.Context) (int64, error) {
	if err := t.write(); err != nil {
		return 0, err
	}
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

func (t *memTx) NamespaceNodeIDs(_ context.Context, namespace string) ([]domain.NodeID, error) {
	namespace = domain.NamespaceOf(namespace)
	var out []domain.NodeID
	for id, vs := range t.st.versions {
		if len(vs) > 0 && domain.NamespaceOf(vs[0].Namespace) == namespace {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (t *memTx) NodeIDsOfType(_ context.Context, namespace, typ string, after domain.NodeID, limit int) ([]domain.NodeID, error) {
	namespace = domain.NamespaceOf(namespace)
	var out []domain.NodeID
	for id, vs := range t.st.versions {
		if len(vs) > 0 && domain.NamespaceOf(vs[0].Namespace) == namespace && vs[0].Type == typ && id > after {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out[:min(len(out), limit)], nil
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

func (t *memTx) PutChange(_ context.Context, c domain.Change) error {
	if err := t.write(); err != nil {
		return err
	}
	c.Items = nil // the facts are in the log
	c.ShareImpacts(domain.Impacts{})
	c.DependsOn = slices.Clone(c.DependsOn)
	t.st.changes[c.ID] = c
	return nil
}

func (t *memTx) AppendLog(_ context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	if err := t.write(); err != nil {
		return domain.LogEntry{}, err
	}
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

// PutCommitImpacts implements Tx.
func (t *memTx) PutCommitImpacts(_ context.Context, rows []CommitImpact) error {
	if err := t.write(); err != nil {
		return err
	}
	for _, r := range rows {
		if _, ok := t.st.changes[r.Change]; !ok {
			return fmt.Errorf("change %s: %w", r.Change, ErrNotFound)
		}
		for _, list := range t.st.impacts {
			for _, x := range list {
				if x.Node == r.Node && x.Version == r.Version {
					return fmt.Errorf("change impact of %s v%d: %w", r.Key, r.Version, ErrConflict)
				}
			}
		}
		t.st.impacts[r.Change] = append(t.st.impacts[r.Change], r)
	}
	return nil
}

func (t *memTx) CommitImpacts(_ context.Context, change domain.ChangeID) ([]CommitImpact, error) {
	out := slices.Clone(t.st.impacts[change])
	slices.SortStableFunc(out, func(a, b CommitImpact) int { return cmp.Compare(a.LogSeq, b.LogSeq) })
	return out, nil
}

// NodeChangePage implements Tx: the changes whose log names the node, the cursor of each the position of its first
// such entry.
func (t *memTx) NodeChangePage(_ context.Context, node domain.NodeID, before int64, limit int) ([]NodeChange, error) {
	first := map[domain.ChangeID]int64{}
	var all []NodeChange
	for _, e := range t.st.log {
		if e.Node != node {
			continue
		}
		if _, ok := first[e.Change]; !ok {
			first[e.Change] = e.Seq
			all = append(all, NodeChange{Change: e.Change, Seq: e.Seq})
		}
	}
	var out []NodeChange
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		if before == 0 || all[i].Seq < before {
			out = append(out, all[i])
		}
	}
	return out, nil
}

func (t *memTx) SetNodeOrigin(_ context.Context, ref domain.NodeRef, change domain.ChangeID, cn domain.ChangeImpactID, comment string) error {
	if err := t.write(); err != nil {
		return err
	}
	vs := t.st.versions[ref.ID]
	if ref.Version < 1 || int(ref.Version) > len(vs) {
		return fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	vs[ref.Version-1].ChangeID, vs[ref.Version-1].ChangeImpact, vs[ref.Version-1].Comment = change, cn, comment
	return nil
}

func (t *memTx) JoinBranch(_ context.Context, ref domain.NodeRef, branch string, change domain.ChangeID, committed int64) error {
	if err := t.write(); err != nil {
		return err
	}
	vs := t.st.versions[ref.ID]
	if ref.Version < 1 || int(ref.Version) > len(vs) {
		return fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	if branch = domain.BranchOf(branch); domain.BranchOf(vs[ref.Version-1].Branch) != branch {
		if _, ok := t.st.joins[joinKey{ref, branch}]; !ok {
			t.st.joins[joinKey{ref, branch}] = memJoin{change: change, committed: committed}
		}
	}
	return nil
}

func (t *memTx) PutTag(_ context.Context, tag domain.Tag) error {
	if err := t.write(); err != nil {
		return err
	}
	if !slices.ContainsFunc(t.st.versions[tag.Node], func(n domain.Node) bool { return n.Version == tag.Version }) {
		return fmt.Errorf("tag %q: node %s: %w", tag.Name, tag.Ref(), ErrNotFound)
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
	if err := t.write(); err != nil {
		return err
	}
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
		if (f.Namespace == "" || tag.Namespace == domain.NamespaceOf(f.Namespace)) && (f.Name == "" || tag.Name == f.Name) && (f.Node == "" || tag.Node == f.Node) {
			out = append(out, tag)
		}
	}
	return out, nil
}

func (t *memTx) DeleteChange(_ context.Context, id domain.ChangeID, namespace, branch string) error {
	if err := t.write(); err != nil {
		return err
	}
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
	var drops []memDrop
	for _, d := range t.st.drops {
		switch {
		case d.change == id, removed(d.from):
		case removed(d.to):
			return used
		default:
			drops = append(drops, d)
		}
	}
	t.st.drops = drops
	for r := range t.st.sets {
		if removed(r) {
			delete(t.st.sets, r)
		}
	}
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
	delete(t.st.impacts, id)
	t.st.tags = slices.DeleteFunc(t.st.tags, func(tag domain.Tag) bool { return removed(tag.Ref()) })
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
	var out []domain.Link
	for _, r := range refs {
		out = append(out, t.held(r)...)
	}
	slices.SortFunc(out, func(a, b domain.Link) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return out, nil
}

func (t *memTx) LinksFromOfType(ctx context.Context, refs []domain.NodeRef, types []string) ([]domain.Link, error) {
	ls, _ := t.LinksFrom(ctx, refs)
	return ofTypes(ls, types), nil
}

func (t *memTx) LinksToOfType(_ context.Context, ref domain.NodeRef, types []string) ([]domain.Link, error) {
	var out []domain.Link
	for _, l := range ofTypes(t.entering(ref.ID, nil), types) {
		if ref.Version == 0 || l.To.Version == ref.Version {
			out = append(out, l)
		}
	}
	return out, nil
}

// ofTypes keeps the links of some types.
func ofTypes(ls []domain.Link, types []string) []domain.Link {
	return slices.DeleteFunc(ls, func(l domain.Link) bool { return !slices.Contains(types, l.Type) })
}

func (t *memTx) LinkSources(_ context.Context, id domain.NodeID) ([]domain.NodeID, error) {
	var out []domain.NodeID
	for _, l := range t.st.links {
		if l.To.ID == id && !slices.Contains(out, l.From.ID) {
			out = append(out, l.From.ID)
		}
	}
	return out, nil
}

func (t *memTx) LinksFromInto(ctx context.Context, refs []domain.NodeRef, to domain.NodeID) ([]domain.Link, error) {
	ls, _ := t.LinksFrom(ctx, refs)
	return slices.DeleteFunc(ls, func(l domain.Link) bool { return l.To.ID != to }), nil
}

func (t *memTx) LinksTo(_ context.Context, id domain.NodeID) ([]domain.Link, error) {
	return t.entering(id, nil), nil
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
	if q.Reach != nil {
		nodes, err := t.NodesAt(ctx, refs, "")
		if err != nil {
			return LinkPage{}, err
		}
		refs = refs[:0:0]
		for _, n := range nodes {
			if q.Reach.Holds(n) {
				refs = append(refs, n.Ref())
			}
		}
	}
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

func (t *memTx) BaselinePage(ctx context.Context, namespace, branch string, before int64, limit int) ([]domain.Baseline, error) {
	all, err := t.BranchBaselineHeaders(ctx, namespace, branch)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Seq > all[j].Seq })
	out := all[:0]
	for _, b := range all {
		if (before == 0 || b.Seq < before) && len(out) < limit {
			out = append(out, b)
		}
	}
	return out, nil
}

func (t *memTx) BaselineHeadersOf(ctx context.Context, namespace string, ids []domain.BaselineID) ([]domain.Baseline, error) {
	all, err := t.BaselineHeaders(ctx, namespace)
	return slices.DeleteFunc(all, func(b domain.Baseline) bool { return !slices.Contains(ids, b.ID) }), err
}

func (t *memTx) ChangeBaselineHeaders(ctx context.Context, namespace string, changes []domain.ChangeID) ([]domain.Baseline, error) {
	all, err := t.BaselineHeaders(ctx, namespace)
	return slices.DeleteFunc(all, func(b domain.Baseline) bool { return !slices.Contains(changes, b.ChangeID) }), err
}

func (t *memTx) BaselineCounts(ctx context.Context, namespace string) (map[string]int, error) {
	all, err := t.BaselineHeaders(ctx, namespace)
	out := map[string]int{}
	for _, b := range all {
		out[domain.BranchOf(b.Branch)]++
	}
	return out, err
}

func (t *memTx) StoreCounts(_ context.Context, namespace string) (map[string]map[string]int, error) {
	namespace = domain.NamespaceOf(namespace)
	out := map[string]map[string]int{}
	for _, m := range StoreMeasures {
		out[m] = map[string]int{}
	}
	in := map[domain.NodeID]bool{}
	for id, vs := range t.st.versions {
		if len(vs) == 0 || vs[0].Namespace != namespace {
			continue
		}
		in[id] = true
		out[StatNodesByType][vs[0].Type]++
		out[StatVersionsPerNode][strconv.Itoa(len(vs))]++
		for _, v := range vs {
			out[StatVersionsByType][v.Type]++
			out[StatVersionsByReason][v.Reason]++
			set := t.setOf(v.Ref())
			switch {
			case set.Of != v.Version:
				out[StatLinkSetsByMode][LinkSetShared]++
			case set.Base != 0:
				out[StatLinkSetsByMode][LinkSetDelta]++
			default:
				out[StatLinkSetsByMode][LinkSetOwn]++
			}
		}
	}
	types := map[domain.LinkID]string{}
	for _, l := range t.st.links {
		types[l.ID] = l.Type
		if in[l.From.ID] {
			out[StatLinkRowsByType][l.Type]++
		}
	}
	for _, d := range t.st.drops {
		if in[d.from.ID] {
			out[StatLinkDrops][types[d.removes]]++
		}
	}
	for k := range t.st.joins {
		if in[k.ref.ID] {
			out[StatJoins][""]++
		}
	}
	for _, b := range t.st.baselines {
		if b.Namespace == namespace {
			out[StatBaselinesByKind][b.Kind]++
		}
	}
	for _, c := range t.st.changes {
		if domain.NamespaceOf(c.Namespace) == namespace {
			out[StatChangesByStatus][string(c.Status)]++
		}
	}
	for _, c := range t.st.changes {
		if domain.NamespaceOf(c.Namespace) != namespace || c.ParentID != "" {
			continue
		}
		var counters map[string]int
		b, _ := json.Marshal(c.Stats.Committed())
		_ = json.Unmarshal(b, &counters)
		for k, n := range counters {
			out[StatChangeCounters][k] += n
		}
	}
	for _, tg := range t.st.tags {
		if tg.Namespace == namespace {
			out[StatTags][tg.Name]++
		}
	}
	for _, e := range t.st.log {
		if c, ok := t.st.changes[e.Change]; ok && domain.NamespaceOf(c.Namespace) == namespace {
			out[StatLogByType][e.Type]++
		}
	}
	return out, nil
}

func (t *memTx) LastCommit(_ context.Context, branch string) (int64, error) {
	branch = domain.BranchOf(branch)
	var last int64
	for _, vs := range t.st.versions {
		for _, v := range vs {
			if domain.BranchOf(v.Branch) == branch {
				last = max(last, v.Committed)
			}
		}
	}
	for k, j := range t.st.joins {
		if k.branch == branch {
			last = max(last, j.committed)
		}
	}
	return last, nil
}
