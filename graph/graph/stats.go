package graph

import (
	"context"
	"math"
	"slices"
	"sort"
	"strconv"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/lnk"
)

// Statistics of the graph (the graph statistics view): what the store holds for a namespace, counted by the database,
// and the shape of the state at the head of a lasting branch (nodes, links, degrees, components), computed from that
// state. Read only, nothing stored.

// Measures of the store (Tx.StoreCounts): each one counts rows of a namespace by a key.
const (
	StatNodesByType      = "nodesByType"      // node rows, by node type
	StatVersionsByType   = "versionsByType"   // node versions, by node type
	StatVersionsByReason = "versionsByReason" // node versions, by reason ("" for an ordinary edit)
	StatVersionsPerNode  = "versionsPerNode"  // nodes, by their number of versions
	StatLinkRowsByType   = "linkRowsByType"   // link rows adding a link, by link type
	StatLinkDrops        = "linkDrops"        // link rows dropping a link of the set they are a delta over (ADR 0141)
	StatLinkSetsByMode   = "linkSetsByMode"   // versions, by how they hold their links: own, shared, delta (ADR 0141)
	StatJoins            = "joins"            // versions joined to another branch than the one they were written on
	StatBaselinesByKind  = "baselinesByKind"  // baselines, by kind (commit, merge, fast-forward...)
	StatChangesByStatus  = "changesByStatus"  // changes, by status
	StatTags             = "tags"             // tags naming a node version
	StatLogByType        = "logByType"        // entries of the logs of the changes, by type
)

// StoreMeasures lists the measures StoreCounts answers, in the order they are shown.
var StoreMeasures = []string{StatNodesByType, StatVersionsByType, StatVersionsByReason, StatVersionsPerNode, StatLinkRowsByType,
	StatLinkDrops, StatLinkSetsByMode, StatJoins, StatBaselinesByKind, StatChangesByStatus, StatTags, StatLogByType}

// Link set modes (StatLinkSetsByMode).
const (
	LinkSetOwn    = "own"    // the version holds its own rows
	LinkSetShared = "shared" // the version holds the set of an earlier version as is
	LinkSetDelta  = "delta"  // the version holds rows over the set of another version
)

// GraphStats are the statistics of a namespace at the head of a branch.
type GraphStats struct {
	Namespace string
	Branch    string
	// Baseline is the header of the head the state was read at; empty ID: nothing committed yet
	Baseline domain.Baseline
	Branches BranchStats
	// Store counts the rows of the namespace, by measure (StoreMeasures) then by key
	Store map[string]map[string]int
	State StateStats
}

// BranchStats counts the branches of a namespace.
type BranchStats struct {
	Total     int
	Lasting   int
	Ephemeral int
	ByStatus  map[string]int
	// Lasting names the lasting branches (main and the named ones), the ones a state is read at
	Names []string
}

// StateStats is the shape of the state at the head of a branch.
type StateStats struct {
	Nodes int
	// Deleted counts the nodes whose version at the head deletes them, left out of everything else
	Deleted int
	// Links between two nodes of the state, Suspect among them the ones reaching a version of their target the state no
	// longer holds (the target changed since); External reach a node outside the state (another namespace, or a node
	// the head deletes) and are counted nowhere else
	Links    int
	Suspect  int
	External int
	// Isolated counts the nodes with no link in or out; SelfLinks the links from a node to itself
	Isolated  int
	SelfLinks int
	// Density is links / (nodes × (nodes − 1)): the share of the possible directed links the state holds
	Density float64
	// Components are the weakly connected components, the largest first (sizes, at most 20); ComponentCount all of them
	ComponentCount int
	Components     []int
	Out            Distribution
	In             Distribution
	Degree         Distribution
	// Histogram counts the nodes by total degree, in buckets
	Histogram []Bucket
	// Versions is the distribution of the version numbers the state holds (how many times its nodes were written)
	Versions Distribution
	// Properties is the distribution of the number of properties set on a node
	Properties Distribution
	Types      []TypeStats
	LinkTypes  []LinkTypeStats
	// Hubs are the nodes with the most links, at most 10; Owners and Projects the distinct units and projects holding nodes
	Hubs     []Hub
	Owners   int
	Projects int
	// Reciprocal counts the pairs of nodes linked both ways
	Reciprocal int
}

// Distribution summarizes a list of counts.
type Distribution struct {
	Min    int
	Max    int
	Mean   float64
	Median float64
	P90    int
	Sum    int
}

// Bucket is a range of a histogram: [From, To], To 0 for no upper bound.
type Bucket struct {
	Label string
	From  int
	To    int
	Count int
}

// TypeStats is the shape of the nodes of one type.
type TypeStats struct {
	Type       string
	Nodes      int
	States     map[string]int
	Out        Distribution
	In         Distribution
	Versions   Distribution
	Properties Distribution
	Isolated   int
}

// LinkTypeStats is the shape of the links of one type.
type LinkTypeStats struct {
	Type  string
	Links int
	// Suspect among Links
	Suspect int
	// Sources and Targets count the distinct nodes the links leave and reach
	Sources int
	Targets int
	// Pairs are the links of the type by the types of their ends, the most frequent first
	Pairs []TypePair
	// MaxOut is the most links of the type a node holds
	MaxOut int
}

// TypePair counts the links from a node type to another.
type TypePair struct {
	From  string
	To    string
	Count int
}

// Hub is a node with many links.
type Hub struct {
	ID   domain.NodeID
	Key  string
	Type string
	Out  int
	In   int
}

// NamespaceSummary is what one namespace holds, for the overview of the graph.
type NamespaceSummary struct {
	Namespace string
	Nodes     int
	Versions  int
	LinkRows  int
	Changes   int
	Baselines int
	Branches  int
	Types     int
	LogItems  int
}

// GraphOverview summarizes every namespace of the graph.
func (g *Graph) GraphOverview(ctx context.Context) (out []NamespaceSummary, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		ns, err := tx.Namespaces(ctx)
		if err != nil {
			return err
		}
		for _, n := range ns {
			counts, err := tx.StoreCounts(ctx, n)
			if err != nil {
				return err
			}
			bs, err := tx.Branches(ctx, n)
			if err != nil {
				return err
			}
			out = append(out, NamespaceSummary{Namespace: n, Nodes: sum(counts[StatNodesByType]), Versions: sum(counts[StatVersionsByType]),
				LinkRows: sum(counts[StatLinkRowsByType]), Changes: sum(counts[StatChangesByStatus]), Baselines: sum(counts[StatBaselinesByKind]),
				Branches: len(bs), Types: len(counts[StatNodesByType]), LogItems: sum(counts[StatLogByType])})
		}
		return nil
	})
	return
}

// GraphStats reads the statistics of a namespace (empty: the default one) at the head of a lasting branch (empty: main).
func (g *Graph) GraphStats(ctx context.Context, namespace, branch string) (s GraphStats, err error) {
	s.Namespace, s.Branch = domain.NamespaceOf(namespace), domain.BranchOf(branch)
	var nodes []domain.Node
	var links []domain.Link
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if s.Store, err = tx.StoreCounts(ctx, s.Namespace); err != nil {
			return err
		}
		bs, err := tx.Branches(ctx, s.Namespace)
		if err != nil {
			return err
		}
		s.Branches = branchStats(bs)
		head, err := branchHead(ctx, tx, s.Namespace, s.Branch)
		if err != nil {
			return err
		}
		s.Baseline = head
		if head.ID == "" || len(head.Nodes) == 0 {
			return nil
		}
		if nodes, err = tx.NodesAt(ctx, refsOf(head.Nodes), ""); err != nil {
			return err
		}
		refs := make([]domain.NodeRef, 0, len(nodes))
		for _, n := range nodes {
			if !n.Deleted {
				refs = append(refs, n.Ref())
			}
		}
		if len(refs) > 0 {
			links, err = tx.LinksFrom(ctx, refs)
		}
		return err
	})
	if err != nil {
		return s, err
	}
	s.State = stateStats(s.Baseline, nodes, links)
	s.Baseline.Nodes = nil
	return s, nil
}

func branchStats(bs []domain.Branch) BranchStats {
	out := BranchStats{Total: len(bs), ByStatus: map[string]int{}}
	for _, b := range bs {
		out.ByStatus[b.Status]++
		if b.Ephemeral {
			out.Ephemeral++
			continue
		}
		out.Lasting++
		out.Names = append(out.Names, b.Name)
	}
	if !slices.Contains(out.Names, domain.MainBranch) {
		out.Names = append(out.Names, domain.MainBranch) // main is there before its first baseline
	}
	sort.Slice(out.Names, func(i, j int) bool {
		// main first, then by name
		if (out.Names[i] == domain.MainBranch) != (out.Names[j] == domain.MainBranch) {
			return out.Names[i] == domain.MainBranch
		}
		return out.Names[i] < out.Names[j]
	})
	return out
}

// stateStats computes the shape of a state from its nodes and the links leaving them.
func stateStats(head domain.Baseline, nodes []domain.Node, links []domain.Link) StateStats {
	var s StateStats
	live := make(map[domain.NodeID]domain.Node, len(nodes))
	for _, n := range nodes {
		if n.Deleted {
			s.Deleted++
			continue
		}
		live[n.ID] = n
	}
	s.Nodes = len(live)
	out := make(map[domain.NodeID]int, len(live))
	in := make(map[domain.NodeID]int, len(live))
	byType := map[string]*LinkTypeStats{}
	sources, targets := map[string]map[domain.NodeID]bool{}, map[string]map[domain.NodeID]bool{}
	pairs := map[string]map[[2]string]int{}
	perNode := map[string]map[domain.NodeID]int{}
	edges := map[[2]domain.NodeID]bool{}
	uf := newUnionFind(live)
	for _, l := range links {
		from, ok := live[l.From.ID]
		if !ok {
			continue
		}
		lt := byType[l.Type]
		if lt == nil {
			lt = &LinkTypeStats{Type: l.Type}
			byType[l.Type] = lt
			sources[l.Type], targets[l.Type], pairs[l.Type], perNode[l.Type] = map[domain.NodeID]bool{}, map[domain.NodeID]bool{}, map[[2]string]int{}, map[domain.NodeID]int{}
		}
		to, inState := live[l.To.ID]
		if v, held := head.Nodes[l.To.ID]; !held || !inState {
			s.External++
			continue
		} else if v != l.To.Version {
			s.Suspect++
			lt.Suspect++
		}
		s.Links++
		lt.Links++
		out[from.ID]++
		in[to.ID]++
		sources[l.Type][from.ID], targets[l.Type][to.ID] = true, true
		pairs[l.Type][[2]string{from.Type, to.Type}]++
		perNode[l.Type][from.ID]++
		if from.ID == to.ID {
			s.SelfLinks++
		}
		edges[[2]domain.NodeID{from.ID, to.ID}] = true
		uf.union(from.ID, to.ID)
	}
	for e := range edges {
		if e[0] < e[1] && edges[[2]domain.NodeID{e[1], e[0]}] {
			s.Reciprocal++
		}
	}
	if s.Nodes > 1 {
		s.Density = float64(s.Links) / (float64(s.Nodes) * float64(s.Nodes-1))
	}
	// the nodes, in a stable order
	ids := make([]domain.NodeID, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return live[ids[i]].Key < live[ids[j]].Key })
	type typeAcc struct {
		out, in, versions, props []int
		states                   map[string]int
		isolated                 int
	}
	types := map[string]*typeAcc{}
	var outs, ins, degrees, versions, props []int
	owners, projects := map[lnk.URL]bool{}, map[lnk.URL]bool{}
	for _, id := range ids {
		n := live[id]
		o, i := out[id], in[id]
		outs, ins, degrees = append(outs, o), append(ins, i), append(degrees, o+i)
		versions, props = append(versions, int(n.Version)), append(props, len(n.Properties))
		t := types[n.Type]
		if t == nil {
			t = &typeAcc{states: map[string]int{}}
			types[n.Type] = t
		}
		t.out, t.in, t.versions, t.props = append(t.out, o), append(t.in, i), append(t.versions, int(n.Version)), append(t.props, len(n.Properties))
		if n.State != "" {
			t.states[n.State]++
		}
		if o+i == 0 {
			s.Isolated++
			t.isolated++
		}
		if !n.Owner.IsZero() {
			owners[n.Owner] = true
		}
		if !n.Project.IsZero() {
			projects[n.Project] = true
		}
	}
	s.Owners, s.Projects = len(owners), len(projects)
	s.Out, s.In, s.Degree = distribution(outs), distribution(ins), distribution(degrees)
	s.Versions, s.Properties = distribution(versions), distribution(props)
	s.Histogram = histogram(degrees)
	for name, t := range types {
		s.Types = append(s.Types, TypeStats{Type: name, Nodes: len(t.out), States: t.states, Out: distribution(t.out), In: distribution(t.in),
			Versions: distribution(t.versions), Properties: distribution(t.props), Isolated: t.isolated})
	}
	sort.Slice(s.Types, func(i, j int) bool {
		if s.Types[i].Nodes != s.Types[j].Nodes {
			return s.Types[i].Nodes > s.Types[j].Nodes
		}
		return s.Types[i].Type < s.Types[j].Type
	})
	for name, lt := range byType {
		lt.Sources, lt.Targets = len(sources[name]), len(targets[name])
		for p, n := range pairs[name] {
			lt.Pairs = append(lt.Pairs, TypePair{From: p[0], To: p[1], Count: n})
		}
		sort.Slice(lt.Pairs, func(i, j int) bool {
			if lt.Pairs[i].Count != lt.Pairs[j].Count {
				return lt.Pairs[i].Count > lt.Pairs[j].Count
			}
			return lt.Pairs[i].From+lt.Pairs[i].To < lt.Pairs[j].From+lt.Pairs[j].To
		})
		for _, n := range perNode[name] {
			lt.MaxOut = max(lt.MaxOut, n)
		}
		s.LinkTypes = append(s.LinkTypes, *lt)
	}
	sort.Slice(s.LinkTypes, func(i, j int) bool {
		if s.LinkTypes[i].Links != s.LinkTypes[j].Links {
			return s.LinkTypes[i].Links > s.LinkTypes[j].Links
		}
		return s.LinkTypes[i].Type < s.LinkTypes[j].Type
	})
	hubs := slices.Clone(ids)
	sort.SliceStable(hubs, func(i, j int) bool { return out[hubs[i]]+in[hubs[i]] > out[hubs[j]]+in[hubs[j]] })
	for _, id := range hubs[:min(10, len(hubs))] {
		if out[id]+in[id] == 0 {
			break
		}
		n := live[id]
		s.Hubs = append(s.Hubs, Hub{ID: id, Key: n.Key, Type: n.Type, Out: out[id], In: in[id]})
	}
	s.Components = uf.sizes()
	s.ComponentCount = len(s.Components)
	s.Components = s.Components[:min(20, len(s.Components))]
	return s
}

// distribution summarizes counts (min, max, mean, median, 90th percentile).
func distribution(xs []int) Distribution {
	if len(xs) == 0 {
		return Distribution{}
	}
	sorted := slices.Clone(xs)
	slices.Sort(sorted)
	d := Distribution{Min: sorted[0], Max: sorted[len(sorted)-1]}
	for _, x := range sorted {
		d.Sum += x
	}
	d.Mean = float64(d.Sum) / float64(len(sorted))
	if n := len(sorted); n%2 == 1 {
		d.Median = float64(sorted[n/2])
	} else {
		d.Median = float64(sorted[n/2-1]+sorted[n/2]) / 2
	}
	d.P90 = sorted[int(math.Ceil(0.9*float64(len(sorted))))-1]
	return d
}

// degreeBuckets are the ranges of the histogram of degrees.
var degreeBuckets = [][2]int{{0, 0}, {1, 1}, {2, 2}, {3, 5}, {6, 10}, {11, 20}, {21, 50}, {51, 100}, {101, 0}}

func histogram(degrees []int) []Bucket {
	out := make([]Bucket, len(degreeBuckets))
	for i, b := range degreeBuckets {
		label := strconv.Itoa(b[0])
		switch {
		case b[1] == 0 && b[0] > 0:
			label += "+"
		case b[1] != b[0]:
			label += "–" + strconv.Itoa(b[1])
		}
		out[i] = Bucket{Label: label, From: b[0], To: b[1]}
	}
	for _, d := range degrees {
		for i := len(degreeBuckets) - 1; i >= 0; i-- {
			if d >= degreeBuckets[i][0] {
				out[i].Count++
				break
			}
		}
	}
	return out
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// unionFind groups the nodes of a state into weakly connected components.
type unionFind struct {
	parent map[domain.NodeID]domain.NodeID
	size   map[domain.NodeID]int
}

func newUnionFind(nodes map[domain.NodeID]domain.Node) *unionFind {
	u := &unionFind{parent: make(map[domain.NodeID]domain.NodeID, len(nodes)), size: make(map[domain.NodeID]int, len(nodes))}
	for id := range nodes {
		u.parent[id], u.size[id] = id, 1
	}
	return u
}

func (u *unionFind) find(id domain.NodeID) domain.NodeID {
	for u.parent[id] != id {
		u.parent[id] = u.parent[u.parent[id]]
		id = u.parent[id]
	}
	return id
}

func (u *unionFind) union(a, b domain.NodeID) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if u.size[ra] < u.size[rb] {
		ra, rb = rb, ra
	}
	u.parent[rb] = ra
	u.size[ra] += u.size[rb]
}

// sizes returns the size of every component, the largest first.
func (u *unionFind) sizes() []int {
	var out []int
	for id, p := range u.parent {
		if id == p {
			out = append(out, u.size[id])
		}
	}
	slices.SortFunc(out, func(a, b int) int { return b - a })
	return out
}
