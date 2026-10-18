package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
	"github.com/zimwip/goap/lib/access"
)

// Modes of writing a change.
const (
	// ModeCommit lands each change with one CommitEdits call (the producer path: the graph creates the change, its
	// drafts, accepts and merges them).
	ModeCommit = "commit"
	// ModeLifecycle drives each change call by call, as the web and the engine do: CreateChange, ImpactNodeCreate or
	// ImpactNodeCheckout + ImpactNodeUpdate per node, ImpactNodeReview by a second principal, CommitChange, MergeChange.
	ModeLifecycle = "lifecycle"
)

// Config is a run of the bench.
type Config struct {
	Run    string      // prefix of the keys of the nodes this run creates (several runs can share one graph)
	Domain DomainShape // size of the domain published at the start
	// DomainEvery publishes a new version of the bench domain (one more type) every that many versions written; 0: only
	// the one at the start.
	DomainEvery int64
	Workers     int     // concurrent writers, each on its own nodes (no conflict between them)
	Edits       int     // nodes per change
	CreateRatio float64 // share of the edits of a change that create a node (the others modify one), 0..1
	Links       int     // links of a created node to earlier nodes
	HotNodes    int     // nodes per worker that take HotRatio of the modifications: deep histories
	HotRatio    float64
	TextBytes   int    // size of the description of a created node
	Mode        string // ModeCommit or ModeLifecycle
	// The run stops at the first of: TargetVersions versions written, Duration elapsed, MaxChanges changes (0: no bound).
	TargetVersions int64
	Duration       time.Duration
	MaxChanges     int64
	ProbeEvery     time.Duration // reads measured against the growing graph (0: none)
	ReportEvery    time.Duration // window of the CSV and of the progress lines
	Seed           uint64
}

// Defaults fills what a config leaves out.
func (c *Config) Defaults() {
	if c.Run == "" {
		c.Run = "r" + strconv.FormatInt(time.Now().Unix()%1_000_000, 36)
	}
	if c.Domain.Types <= 0 {
		c.Domain.Types = 10
	}
	c.Workers = max(c.Workers, 1)
	c.Edits = max(c.Edits, 1)
	if c.Mode == "" {
		c.Mode = ModeCommit
	}
	if c.ReportEvery <= 0 {
		c.ReportEvery = 10 * time.Second
	}
	if c.Seed == 0 {
		c.Seed = uint64(time.Now().UnixNano())
	}
}

// Runner runs a config against the APIs.
type Runner struct {
	Config   Config
	Clients  Clients // the writer
	Reviewer Clients // the reviewer of the lifecycle mode (a verifier is not the producer, ADR 0075)
	Metrics  *Metrics
	CSV      *CSVWriter // nil: no report file
	Log      io.Writer

	start   time.Time
	stop    context.CancelCauseFunc // ends the run when the writers only fail
	types   int                     // node types available to the writers (those of the first publication)
	step    int                     // last publication step of the bench domain
	workers []*worker
}

type node struct {
	key, typ string
	id       string
	version  int32
}

type worker struct {
	id     int
	rnd    *rand.Rand
	failed int // changes in a row that failed
	seq    int
	mu     sync.Mutex // the probes read the nodes
	all    []*node
}

// Run publishes the domain, then writes and probes until a bound is reached or ctx ends.
func (r *Runner) Run(ctx context.Context) error {
	r.Config.Defaults()
	cfg := r.Config
	r.start = time.Now()
	if err := r.publishDomain(ctx, true); err != nil {
		return fmt.Errorf("publish the bench domain: %w", err)
	}
	r.types = cfg.Domain.Types + r.step
	r.logf("run %s: %d workers, %d edits per change (%.0f%% creations), mode %s, domain %s with %d types",
		cfg.Run, cfg.Workers, cfg.Edits, 100*cfg.CreateRatio, cfg.Mode, DomainVersion(r.step), r.types)

	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	r.stop = stop
	if cfg.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Duration)
		defer cancel()
	}
	var bg sync.WaitGroup
	bg.Go(func() { r.report(ctx) })
	if cfg.ProbeEvery > 0 {
		bg.Go(func() { r.probe(ctx) })
	}
	if cfg.DomainEvery > 0 {
		bg.Go(func() { r.growDomain(ctx) })
	}
	var wg sync.WaitGroup
	for i := range cfg.Workers {
		w := &worker{id: i, rnd: rand.New(rand.NewPCG(cfg.Seed, uint64(i)))}
		r.workers = append(r.workers, w)
		wg.Go(func() {
			for ctx.Err() == nil && !r.done() {
				r.change(ctx, w)
			}
		})
	}
	wg.Wait()
	failed := context.Cause(ctx)
	stop(nil)
	bg.Wait()
	r.flush() // the last window
	r.logf("done in %s: %d versions, %d nodes, %d changes, %d domain versions, %d errors", time.Since(r.start).Round(time.Second),
		r.Metrics.Totals.Versions.Load(), r.Metrics.Totals.Nodes.Load(), r.Metrics.Totals.Changes.Load(), r.Metrics.Totals.DomainVersions.Load(), r.Metrics.Totals.Errors.Load())
	var fatal *failing
	if errors.As(failed, &fatal) {
		return fatal
	}
	return nil
}

// maxFailures is the number of changes in a row a writer may fail before the run stops.
const maxFailures = 10

// failing is the end of a run whose writers only fail.
type failing struct{ last error }

func (f *failing) Error() string {
	return fmt.Sprintf("%d changes in a row failed, the last: %v", maxFailures, f.last)
}

func (r *Runner) done() bool {
	t := &r.Metrics.Totals
	return (r.Config.TargetVersions > 0 && t.Versions.Load() >= r.Config.TargetVersions) ||
		(r.Config.MaxChanges > 0 && t.Changes.Load() >= r.Config.MaxChanges)
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		fmt.Fprintf(r.Log, format+"\n", args...)
	}
}

// publishDomain imports and publishes the next version of the bench domain (the first run: 1.0.0).
func (r *Runner) publishDomain(ctx context.Context, first bool) error {
	if first {
		list, err := timed(ctx, r.Metrics, "ListDomains", func(ctx context.Context) (*connect.Response[registryv1.ListDomainsResponse], error) {
			return r.Clients.Domains.ListDomains(ctx, connect.NewRequest(&registryv1.ListDomainsRequest{AllVersions: true}))
		})
		if err != nil {
			return err
		}
		r.step = -1
		for _, d := range list.Msg.Domains {
			if d.Name != Namespace {
				continue
			}
			if n, ok := strings.CutPrefix(d.Version, "1.0."); ok {
				if s, err := strconv.Atoi(n); err == nil && s > r.step {
					r.step = s
				}
			}
		}
		r.Metrics.Totals.DomainVersions.Store(int64(r.step + 1))
	}
	next := r.step + 1
	if _, err := timed(ctx, r.Metrics, "ImportDomain", func(ctx context.Context) (*connect.Response[registryv1.ImportDomainResponse], error) {
		return r.Clients.Domains.ImportDomain(ctx, connect.NewRequest(&registryv1.ImportDomainRequest{Yaml: DomainYAML(r.Config.Domain, next), Publish: true}))
	}); err != nil {
		return err
	}
	r.step = next
	r.Metrics.Totals.DomainVersions.Add(1)
	r.Metrics.Totals.DomainTypes.Store(int64(r.Config.Domain.Types + next))
	return nil
}

// growDomain publishes a version of the domain each time DomainEvery more versions are written.
func (r *Runner) growDomain(ctx context.Context) {
	mark := r.Metrics.Totals.Versions.Load()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if v := r.Metrics.Totals.Versions.Load(); v-mark >= r.Config.DomainEvery {
			mark = v
			if err := r.publishDomain(ctx, false); err != nil && ctx.Err() == nil {
				r.logf("publish domain %s: %v", DomainVersion(r.step+1), err)
			}
		}
	}
}

func (r *Runner) report(ctx context.Context) {
	t := time.NewTicker(r.Config.ReportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.flush()
		}
	}
}

func (r *Runner) flush() {
	rows := r.Metrics.Flush(time.Since(r.start), r.Config.ReportEvery)
	if len(rows) == 0 {
		return
	}
	r.logf("%s", Summary(rows, r.Config.ReportEvery))
	if r.CSV != nil {
		if err := r.CSV.Write(rows); err != nil {
			r.logf("csv: %v", err)
		}
	}
}

// edit is one node of a change being built.
type edit struct {
	n      *node
	create bool
	props  map[string]any
	links  []*node
}

// plan picks the nodes of the next change of a worker.
func (r *Runner) plan(w *worker) []edit {
	cfg := r.Config
	w.mu.Lock()
	defer w.mu.Unlock()
	picked := map[*node]bool{}
	var edits []edit
	for range cfg.Edits {
		if len(w.all) == 0 || w.rnd.Float64() < cfg.CreateRatio {
			w.seq++
			n := &node{key: fmt.Sprintf("%s-w%02d-%08d", cfg.Run, w.id, w.seq), typ: TypeName(w.rnd.IntN(r.types))}
			props := map[string]any{"title": "Node " + n.key, "description": text(w.rnd, cfg.TextBytes), "value": w.rnd.Float64() * 1000, "rev": 0.0}
			for a := range cfg.Domain.Attributes {
				props["a"+strconv.Itoa(a)] = "v" + strconv.Itoa(w.rnd.IntN(1000))
			}
			e := edit{n: n, create: true, props: props}
			for range cfg.Links {
				if len(w.all) > 0 {
					e.links = append(e.links, w.all[w.rnd.IntN(len(w.all))])
				}
			}
			edits = append(edits, e)
			continue
		}
		var n *node
		for range 4 { // a node not picked yet in this change
			if hot := min(cfg.HotNodes, len(w.all)); hot > 0 && w.rnd.Float64() < cfg.HotRatio {
				n = w.all[w.rnd.IntN(hot)]
			} else {
				n = w.all[w.rnd.IntN(len(w.all))]
			}
			if !picked[n] {
				break
			}
		}
		if picked[n] {
			continue
		}
		picked[n] = true
		edits = append(edits, edit{n: n, props: map[string]any{"value": w.rnd.Float64() * 1000, "rev": float64(n.version)}})
	}
	return edits
}

// change writes one change of a worker.
func (r *Runner) change(ctx context.Context, w *worker) {
	edits := r.plan(w)
	for _, e := range edits {
		if !e.create && e.n.id == "" {
			if err := r.resolve(ctx, e.n); err != nil {
				return
			}
		}
	}
	var err error
	if r.Config.Mode == ModeLifecycle {
		err = r.lifecycle(ctx, w, edits)
	} else {
		err = r.commit(ctx, w, edits)
	}
	if err != nil {
		if ctx.Err() == nil && !isConflict(err) {
			r.logf("worker %d: %v", w.id, err)
			if w.failed++; w.failed >= maxFailures {
				r.stop(&failing{last: err})
			}
			time.Sleep(time.Second) // a failing service is not hammered
		}
		for _, e := range edits {
			if !e.create {
				e.n.id = "" // read again: its version may have moved
			}
		}
		return
	}
	w.failed = 0
	r.landed(w, edits)
}

func (r *Runner) landed(w *worker, edits []edit) {
	t := &r.Metrics.Totals
	w.mu.Lock()
	for _, e := range edits {
		if e.create {
			e.n.version = 1
			w.all = append(w.all, e.n)
			t.Nodes.Add(1)
			t.Links.Add(int64(len(e.links)))
		} else {
			e.n.version++
		}
	}
	w.mu.Unlock()
	t.Versions.Add(int64(len(edits)))
	t.Changes.Add(1)
}

// resolve reads the id and latest version of a node created by the bench.
func (r *Runner) resolve(ctx context.Context, n *node) error {
	res, err := timed(ctx, r.Metrics, "resolve.GetNode", func(ctx context.Context) (*connect.Response[graphv1.GetNodeResponse], error) {
		return r.Clients.Graph.GetNode(ctx, connect.NewRequest(&graphv1.GetNodeRequest{Namespace: Namespace, Key: n.key}))
	})
	if err != nil {
		return err
	}
	n.id, n.version = res.Msg.View.GetNode().GetId(), res.Msg.View.GetNode().GetVersion()
	return nil
}

func (r *Runner) commit(ctx context.Context, w *worker, edits []edit) error {
	req := &graphv1.CommitEditsRequest{Namespace: Namespace, Title: fmt.Sprintf("bench %s w%d", r.Config.Run, w.id), ProjectId: access.DefaultProject}
	for _, e := range edits {
		pe := &graphv1.NodeEdit{Props: structOf(e.props)}
		if e.create {
			pe.Key, pe.Type = e.n.key, e.n.typ
			for _, to := range e.links {
				pe.Links = append(pe.Links, &graphv1.LinkEdit{Type: LinkType, ToKey: to.key})
			}
		} else {
			pe.Pre = &graphv1.NodeRef{Id: e.n.id, Version: e.n.version}
		}
		req.Edits = append(req.Edits, pe)
	}
	_, err := timed(ctx, r.Metrics, "CommitEdits", func(ctx context.Context) (*connect.Response[graphv1.CommitEditsResponse], error) {
		return r.Clients.Graph.CommitEdits(ctx, connect.NewRequest(req))
	})
	return err
}

func (r *Runner) lifecycle(ctx context.Context, w *worker, edits []edit) error {
	g, m := r.Clients.Graph, r.Metrics
	ch, err := timed(ctx, m, "CreateChange", func(ctx context.Context) (*connect.Response[graphv1.CreateChangeResponse], error) {
		return g.CreateChange(ctx, connect.NewRequest(&graphv1.CreateChangeRequest{Namespace: Namespace, Title: fmt.Sprintf("bench %s w%d", r.Config.Run, w.id), ProjectId: access.DefaultProject}))
	})
	if err != nil {
		return err
	}
	id := ch.Msg.Change.GetId()
	abandon := func(err error) error {
		_, _ = g.DeleteChange(ctx, connect.NewRequest(&graphv1.DeleteChangeRequest{ChangeId: id}))
		return err
	}
	impacts := make([]string, 0, len(edits))
	for _, e := range edits {
		if e.create {
			var links []*graphv1.NodeLinkWrite
			for _, to := range e.links {
				if to.id != "" {
					links = append(links, &graphv1.NodeLinkWrite{Type: LinkType, To: &graphv1.NodeRef{Id: to.id, Version: to.version}})
				}
			}
			res, err := timed(ctx, m, "ImpactNodeCreate", func(ctx context.Context) (*connect.Response[graphv1.ImpactNodeCreateResponse], error) {
				return g.ImpactNodeCreate(ctx, connect.NewRequest(&graphv1.ImpactNodeCreateRequest{ChangeId: id, Key: e.n.key, Type: e.n.typ, Props: structOf(e.props), Links: links, Rationale: "bench: created"}))
			})
			if err != nil {
				return abandon(err)
			}
			impacts = append(impacts, res.Msg.Node.GetId())
			continue
		}
		res, err := timed(ctx, m, "ImpactNodeCheckout", func(ctx context.Context) (*connect.Response[graphv1.ImpactNodeCheckoutResponse], error) {
			return g.ImpactNodeCheckout(ctx, connect.NewRequest(&graphv1.ImpactNodeCheckoutRequest{ChangeId: id, NodeId: e.n.id, Rationale: "bench: modified"}))
		})
		if err != nil {
			return abandon(err)
		}
		imp := res.Msg.Node.GetId()
		if _, err := timed(ctx, m, "ImpactNodeUpdate", func(ctx context.Context) (*connect.Response[graphv1.ImpactNodeUpdateResponse], error) {
			return g.ImpactNodeUpdate(ctx, connect.NewRequest(&graphv1.ImpactNodeUpdateRequest{ChangeId: id, ChangeImpactId: imp, Props: structOf(e.props)}))
		}); err != nil {
			return abandon(err)
		}
		impacts = append(impacts, imp)
	}
	for _, imp := range impacts {
		if _, err := timed(ctx, m, "ImpactNodeReview", func(ctx context.Context) (*connect.Response[graphv1.ImpactNodeReviewResponse], error) {
			return r.Reviewer.Graph.ImpactNodeReview(ctx, connect.NewRequest(&graphv1.ImpactNodeReviewRequest{ChangeId: id, ChangeImpactId: imp, Accept: true, Comment: "bench"}))
		}); err != nil {
			return abandon(err)
		}
	}
	if _, err := timed(ctx, m, "CommitChange", func(ctx context.Context) (*connect.Response[graphv1.CommitChangeResponse], error) {
		return g.CommitChange(ctx, connect.NewRequest(&graphv1.CommitChangeRequest{ChangeId: id}))
	}); err != nil {
		return abandon(err)
	}
	if _, err := timed(ctx, m, "MergeChange", func(ctx context.Context) (*connect.Response[graphv1.MergeChangeResponse], error) {
		return g.MergeChange(ctx, connect.NewRequest(&graphv1.MergeChangeRequest{ChangeId: id}))
	}); err != nil {
		return err // committed: it stays, as a change waiting for its merge
	}
	return nil
}

// probe measures the reads of a client against the growing graph, every ProbeEvery.
func (r *Runner) probe(ctx context.Context) {
	t := time.NewTicker(r.Config.ProbeEvery)
	defer t.Stop()
	rnd := rand.New(rand.NewPCG(r.Config.Seed, 1<<32))
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.probeOnce(ctx, rnd)
	}
}

// sampleNode is a node of the run with a known id, hot (the first of a worker: the deepest history) or any.
func (r *Runner) sampleNode(rnd *rand.Rand, hot bool) *node {
	if len(r.workers) == 0 {
		return nil
	}
	w := r.workers[rnd.IntN(len(r.workers))]
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.all) == 0 {
		return nil
	}
	n := w.all[0]
	if !hot {
		n = w.all[rnd.IntN(len(w.all))]
	}
	c := *n
	return &c
}

func (r *Runner) probeOnce(ctx context.Context, rnd *rand.Rand) {
	g, m := r.Clients.Graph, r.Metrics
	// the node is sampled before the head is read: one landed later is not in that head
	sampled := r.sampleNode(rnd, false)
	br, err := timed(ctx, m, "GetBranch", func(ctx context.Context) (*connect.Response[graphv1.GetBranchResponse], error) {
		return g.GetBranch(ctx, connect.NewRequest(&graphv1.GetBranchRequest{Namespace: Namespace, Name: "main"}))
	})
	if err != nil {
		return
	}
	head := br.Msg.Head.GetId()
	if n := sampled; n != nil {
		if v, err := timed(ctx, m, "GetNode", func(ctx context.Context) (*connect.Response[graphv1.GetNodeResponse], error) {
			return g.GetNode(ctx, connect.NewRequest(&graphv1.GetNodeRequest{Namespace: Namespace, Key: n.key}))
		}); err == nil {
			_, _ = timed(ctx, m, "GetNodeNeighbourhood", func(ctx context.Context) (*connect.Response[graphv1.GetNodeNeighbourhoodResponse], error) {
				return g.GetNodeNeighbourhood(ctx, connect.NewRequest(&graphv1.GetNodeNeighbourhoodRequest{BaselineId: head, NodeId: v.Msg.View.GetNode().GetId()}))
			})
		}
	}
	if n := r.sampleNode(rnd, true); n != nil {
		if v, err := timed(ctx, m, "hot.GetNode", func(ctx context.Context) (*connect.Response[graphv1.GetNodeResponse], error) {
			return g.GetNode(ctx, connect.NewRequest(&graphv1.GetNodeRequest{Namespace: Namespace, Key: n.key}))
		}); err == nil {
			id := v.Msg.View.GetNode().GetId()
			_, _ = timed(ctx, m, "ListNodeVersions", func(ctx context.Context) (*connect.Response[graphv1.ListNodeVersionsResponse], error) {
				return g.ListNodeVersions(ctx, connect.NewRequest(&graphv1.ListNodeVersionsRequest{Id: id}))
			})
			_, _ = timed(ctx, m, "ListNodeChanges", func(ctx context.Context) (*connect.Response[graphv1.ListNodeChangesResponse], error) {
				return g.ListNodeChanges(ctx, connect.NewRequest(&graphv1.ListNodeChangesRequest{NodeId: id}))
			})
		}
	}
	_, _ = timed(ctx, m, "ListBaselineNodes", func(ctx context.Context) (*connect.Response[graphv1.ListBaselineNodesResponse], error) {
		return g.ListBaselineNodes(ctx, connect.NewRequest(&graphv1.ListBaselineNodesRequest{BaselineId: head, Type: TypeName(rnd.IntN(max(r.types, 1))), Limit: 100}))
	})
	_, _ = timed(ctx, m, "search.ListBaselineNodes", func(ctx context.Context) (*connect.Response[graphv1.ListBaselineNodesResponse], error) {
		return g.ListBaselineNodes(ctx, connect.NewRequest(&graphv1.ListBaselineNodesRequest{BaselineId: head, Query: fmt.Sprintf("w%02d-%07d", rnd.IntN(max(r.Config.Workers, 1)), rnd.IntN(1000)), Limit: 50}))
	})
	if parent := br.Msg.Head.GetParentId(); parent != "" {
		_, _ = timed(ctx, m, "DiffBaselines", func(ctx context.Context) (*connect.Response[graphv1.DiffBaselinesResponse], error) {
			return g.DiffBaselines(ctx, connect.NewRequest(&graphv1.DiffBaselinesRequest{From: parent, To: head}))
		})
	}
	_, _ = timed(ctx, m, "ListChanges", func(ctx context.Context) (*connect.Response[graphv1.ListChangesResponse], error) {
		return g.ListChanges(ctx, connect.NewRequest(&graphv1.ListChangesRequest{Namespace: Namespace, Status: []string{"active"}}))
	})
}

// text is a pseudo-random text of n bytes.
func text(rnd *rand.Rand, n int) string {
	if n <= 0 {
		return ""
	}
	words := []string{"graph", "change", "domain", "version", "node", "link", "baseline", "branch", "review", "impact", "merge", "draft"}
	var b strings.Builder
	for b.Len() < n {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(words[rnd.IntN(len(words))])
	}
	return b.String()[:n]
}

func structOf(m map[string]any) *structpb.Struct {
	s, err := structpb.NewStruct(m)
	if err != nil {
		panic(errors.Join(errors.New("bench: properties"), err)) // only plain values are built
	}
	return s
}

// WaitReady waits until the graph service answers and the platform is bootstrapped (the root project exists), up to d.
func (r *Runner) WaitReady(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		_, err := r.Clients.Graph.GetNode(ctx, connect.NewRequest(&graphv1.GetNodeRequest{Namespace: access.NamespaceOrganisation, Key: access.DefaultProject}))
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the graph service is not ready after %s: %w", d, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
