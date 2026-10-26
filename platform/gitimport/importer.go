// Package gitimport loads a git repository into the graph, in the code domain (domains/code.yaml, ADR 0147): one change
// per commit of the first-parent history, a node per directory and per file, a directory holding its entries by
// contains links. It reads git through the git command (one streaming git log for the whole history, one cat-file for
// the sizes) and writes through the graph's operations on a change, so it works over the graph service (graphclient)
// or a graph in process alike; it resumes where the last import stopped (the commit the repository node names).
package gitimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
)

// The code domain.
const (
	Namespace     = "code"
	TypeRepo      = "code@Repository"
	TypeDirectory = "code@Directory"
	TypeFile      = "code@File"
	TypeSymbol    = "code@Symbol"
	TypeFunction  = "code@Function"
	TypeMethod    = "code@Method"
	TypeClass     = "code@Class"
	TypeStruct    = "code@Struct"
	TypeInterface = "code@Interface"
	TypeEnum      = "code@Enum"
	TypeType      = "code@Type"
	TypeVariable  = "code@Variable"
	TypeConstant  = "code@Constant"
	TypeField     = "code@Field"
	TypePackage   = "code@Package"
	LinkContains  = "code@contains"
	LinkDefines   = "code@defines"
	LinkMemberOf  = "code@member_of"
	LinkCalls     = "code@calls"
	LinkUses      = "code@uses"
	LinkInPackage = "code@in_package"
	LinkImports   = "code@imports"
	LinkDependsOn = "code@depends_on"
	StatePresent  = "present"
	StateRemoved  = "removed"
)

// Port is what the importer asks of the graph: open a change, give it its node operations, review and apply it, read
// the heads it follows, and open the branches of the git branches (*graph.Graph and graphclient.Client).
type Port interface {
	CreateChange(ctx context.Context, in graphapi.NewChange) (domain.Change, error)
	UpdateChange(ctx context.Context, id domain.ChangeID, p graphapi.ChangePatch) (domain.Change, error)
	ImpactNodeCreate(ctx context.Context, id domain.ChangeID, in graphapi.NodeCreate) (domain.ChangeImpact, error)
	ImpactNodeCheckout(ctx context.Context, id domain.ChangeID, in graphapi.NodeCheckout) (domain.ChangeImpact, error)
	ImpactNodeUpdate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, in graphapi.NodeUpdate) (domain.ChangeImpact, error)
	ImpactLinkCreate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, l graphapi.LinkWrite, flow, execution string) (domain.Link, error)
	ImpactLinkDelete(ctx context.Context, id domain.ChangeID, link domain.LinkID, flow, execution string) error
	ImpactNodeTransition(ctx context.Context, id domain.ChangeID, in graphapi.NodeTransition) (domain.ChangeImpact, error)
	ImpactNodeReviewBatch(ctx context.Context, id domain.ChangeID, b domain.ReviewBatch) ([]domain.ChangeImpact, error)
	CommitChange(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error)
	CloseChange(ctx context.Context, id domain.ChangeID) (domain.Change, error)
	Change(ctx context.Context, id domain.ChangeID) (domain.Change, error)
	BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error)
	Branch(ctx context.Context, namespace, name string) (domain.Branch, error)
	CreateBranch(ctx context.Context, in graphapi.NewBranch) (domain.Branch, error)
	VersionPage(ctx context.Context, id domain.NodeID, before domain.Version, limit int) ([]domain.Node, domain.Version, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
	BaselineDelta(ctx context.Context, from, to domain.BaselineID) (graphapi.BaselineDelta, error)
	SuspectLinks(ctx context.Context, id domain.BaselineID) ([]domain.Link, error)
}

// Importer imports one repository.
type Importer struct {
	Graph Port
	// Dir is the git repository read (a work tree or a bare repository).
	Dir string
	// Name names the repository in the graph: the key of its node and the prefix of the keys of its entries.
	Name string
	// Source is recorded on the repository node (default: Dir).
	Source string
	// Rev is the revision followed on the main branch of the graph (default HEAD).
	Rev string
	// Branches names the other git branches imported (ADR 0151), each on a branch of the graph of its own named
	// <Name>/<git branch>, forked from the state of the last commit of its first-parent line already imported (on main
	// or on the branch of another git branch): a name, a pattern (path.Match) or "*" for every branch; the branch Rev
	// follows is main's. A branch forked before the first commit imported (Base) is left out, an orphan branch starts
	// from the empty state, a branch rewritten since its last import (its last commit imported no longer in its history)
	// is left out (Stats.Skipped).
	Branches []string
	// Base, for a first import, starts at that revision: its whole tree is the first change, then the commits after it
	// (the history of the Linux kernel before a release left out). Empty: from the root commit.
	Base string
	// Max bounds the number of commits of this run (0: no bound).
	Max int
	// ProjectID is the project the changes act in (required, ADR 0091); OwnerOrg the unit holding them (empty: the root).
	ProjectID, OwnerOrg string
	// By is who accepts the changes (default "gitimport").
	By string
	// Symbols, when set, reads the source files a commit adds or changes (NewTreeSitter): each declaration a node its
	// file defines, linked to the declarations it is a member of, calls and uses, and each file in its package, linked
	// to the packages it imports. MaxSymbolBytes leaves the larger files out (default 1 MiB); Code keeps the source
	// text of each declaration on its node; Parallel is how many files are parsed at once (default: the processors,
	// at most 8).
	Symbols        Extractor
	MaxSymbolBytes int64
	Code           bool
	Parallel       int
	// Batch is the most operations on nodes the change of a commit takes by itself (default 1000): a commit doing more
	// is written by sub-changes of its change, one per file (the files naming each other's declarations together,
	// the packages first, the directories last), each merged into it in turn (an operation on a change costs in
	// proportion to the impacts it holds).
	Batch int
	// Progress, when set, receives a line every ProgressEvery commits (default 100) and at the end.
	Progress      io.Writer
	ProgressEvery int
	// OnPhase, when set, says what the run does before its commits (reading the graph, counting the commits).
	OnPhase func(string)
	// OnBegin, when set, is called before each commit is imported, with what the run did so far (Stats.Total known).
	OnBegin func(Stats, Commit)
	// OnCommit, when set, is called after each commit imported with what the run did so far.
	OnCommit func(Stats)
	// OnPart, when set, is called before each sub-change of a commit written by sub-changes: its number, how many, what
	// it writes.
	OnPart func(i, n int, name string)

	ix      index
	head    domain.BaselineID
	branch  string            // the branch of the graph the commits are imported on
	gitRef  string            // the git branch it follows ("" when unknown)
	modules map[string]string // the module paths of the go.mod files, by blob
	sizes   *Sizes
	blobs   *Blobs
	stats   Stats
	start   time.Time
}

// Stats is what a run did.
type Stats struct {
	Commits                    int
	Created, Modified, Removed int
	// Symbols and Packages count the symbols and the packages created, modified or removed (among the nodes counted
	// above).
	Symbols, Packages int
	// Last is the last commit imported.
	Last string
	// Total is the number of commits this run imports (known once it counted them, the first tree of Base included;
	// the commits of a git branch counted as those not on Rev's line until the run reaches it).
	Total int
	// Branches is the number of git branches imported besides Rev's; Skipped says which were left out and why.
	Branches int
	Skipped  []string
}

// Run imports the commits not imported yet and returns what it did.
func (im *Importer) Run(ctx context.Context) (Stats, error) {
	im.stats = Stats{}
	st, err := im.run(ctx)
	// a cancelled run says so, whatever its cancel broke first (a git process killed, a transaction refused)
	if err != nil && ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
		err = fmt.Errorf("%w: %v", context.Cause(ctx), err)
	}
	return st, err
}

func (im *Importer) run(ctx context.Context) (Stats, error) {
	if im.Name == "" || strings.ContainsAny(im.Name, ":/@") {
		return im.stats, fmt.Errorf("gitimport: the repository needs a name without :, / nor @, got %q", im.Name)
	}
	if im.ProjectID == "" {
		return im.stats, errors.New("gitimport: a project is required")
	}
	if im.Rev == "" {
		im.Rev = "HEAD"
	}
	if im.Source == "" {
		im.Source = im.Dir
	}
	if im.By == "" {
		im.By = "gitimport"
	}
	if im.ProgressEvery <= 0 {
		im.ProgressEvery = 100
	}
	im.start = time.Now()
	rev, err := Resolve(ctx, im.Dir, im.Rev)
	if err != nil {
		return im.stats, err
	}
	lines, err := im.lines(ctx)
	if err != nil {
		return im.stats, err
	}
	im.branch, im.gitRef = domain.MainBranch, BranchOf(ctx, im.Dir, im.Rev)
	im.phase("reading what the graph holds of " + im.Name)
	if err := im.load(ctx); err != nil {
		return im.stats, err
	}
	sizes, err := NewSizes(ctx, im.Dir)
	if err != nil {
		return im.stats, err
	}
	im.sizes = sizes
	defer sizes.Close()
	if im.Symbols != nil {
		if im.MaxSymbolBytes <= 0 {
			im.MaxSymbolBytes = 1 << 20
		}
		blobs, err := NewBlobs(ctx, im.Dir)
		if err != nil {
			return im.stats, err
		}
		im.blobs = blobs
		defer blobs.Close()
	}
	from := im.ix.commit
	if err := im.count(ctx, from, rev); err != nil {
		return im.stats, err
	}
	for i, l := range lines {
		if lines[i].estimate, err = Count(ctx, im.Dir, rev, l.tip); err != nil {
			return im.stats, err
		}
		im.stats.Total += lines[i].estimate
	}
	if im.Max > 0 {
		im.stats.Total = min(im.stats.Total, im.Max)
	}
	if err := im.main(ctx, from, rev); err != nil {
		im.report(true)
		return im.stats, err
	}
	for _, l := range lines {
		if err := im.line(ctx, l); err != nil {
			im.report(true)
			return im.stats, err
		}
	}
	im.report(true)
	return im.stats, nil
}

// main imports the commits of Rev after from on the main branch.
func (im *Importer) main(ctx context.Context, from, rev string) error {
	if from == "" && im.Base != "" {
		c, err := Tree(ctx, im.Dir, im.Base)
		if err != nil {
			return err
		}
		if err := im.commit(ctx, c); err != nil {
			return err
		}
		from = c.Hash
	}
	return im.history(ctx, from, rev)
}

// history imports the commits of the first-parent line of rev after from on the current branch, within Max.
func (im *Importer) history(ctx context.Context, from, rev string) error {
	if from == rev {
		return nil
	}
	max := im.Max
	if max > 0 && im.stats.Commits >= max {
		return nil
	} else if max > 0 {
		max -= im.stats.Commits
	}
	return Log(ctx, im.Dir, from, rev, max, func(c Commit) error { return im.commit(ctx, c) })
}

func (im *Importer) phase(what string) {
	if im.OnPhase != nil {
		im.OnPhase(what)
	}
}

// count gives Stats.Total: the commits after from up to rev (bounded by Max), and the first tree of Base.
func (im *Importer) count(ctx context.Context, from, rev string) error {
	im.phase("counting the commits of " + im.Name)
	base := 0
	if from == "" && im.Base != "" {
		b, err := Resolve(ctx, im.Dir, im.Base)
		if err != nil {
			return err
		}
		from, base = b, 1
	}
	n := 0
	if from != rev {
		var err error
		if n, err = Count(ctx, im.Dir, from, rev); err != nil {
			return err
		}
	}
	n += base
	if im.Max > 0 && n > im.Max {
		n = im.Max
	}
	im.stats.Total = n
	return nil
}

func (im *Importer) report(final bool) {
	if im.Progress == nil || (!final && im.stats.Commits%im.ProgressEvery != 0) {
		return
	}
	el := time.Since(im.start)
	rate := float64(im.stats.Commits) / el.Seconds()
	fmt.Fprintf(im.Progress, "%s: %d commits (%.1f/s), %d nodes created, %d modified, %d removed (%d symbols, %d packages), at %s\n",
		im.Name, im.stats.Commits, rate, im.stats.Created, im.stats.Modified, im.stats.Removed, im.stats.Symbols, im.stats.Packages, short(im.stats.Last))
}

// ---- the repository as the graph holds it ----

// entry is a node of the repository at the head.
type entry struct {
	id      domain.NodeID
	typ     string
	ver     domain.Version
	removed bool
	// links are its outgoing links: the entries a directory holds, the symbols a file defines, its package...
	links map[domain.LinkID]link
	out   map[linkKey]domain.LinkID // the same by type and target
	// sum is the fingerprint of the attributes of a symbol or a package (git says when a directory or a file changed,
	// not of these).
	sum string
	// blob is the content of a file; name the name of a package; sym a symbol as the resolution sees it.
	blob, name string
	sym        *symInfo
}

// link is an outgoing link of an entry: its type and the version of the node it targets.
type link struct {
	typ string
	to  domain.NodeID
	ver domain.Version
}

// linkKey is a link by type and target node.
type linkKey struct {
	typ string
	to  domain.NodeID
}

// linkTo is the link of a type to a node, "" when none.
func (e *entry) linkTo(typ string, to domain.NodeID) domain.LinkID {
	if e == nil {
		return ""
	}
	return e.out[linkKey{typ, to}]
}

// index is the repository at the head of the namespace, by key: read once whole, then followed commit by commit by the
// delta of each landing.
type index struct {
	byKey  map[string]*entry
	keyOf  map[domain.NodeID]string
	commit string // the last commit imported (the repository node's)
	// into are the links entering a node: target → link → source.
	into map[domain.NodeID]map[domain.LinkID]domain.NodeID
	// fileSyms are the symbols of each file (by key), gomods the go.mod files by directory (their blob).
	fileSyms map[string]map[string]bool
	gomods   map[string]string
}

func (im *Importer) load(ctx context.Context) error {
	im.ix = index{byKey: map[string]*entry{}, keyOf: map[domain.NodeID]string{}, into: map[domain.NodeID]map[domain.LinkID]domain.NodeID{},
		fileSyms: map[string]map[string]bool{}, gomods: map[string]string{}}
	head, err := im.Graph.BranchHead(ctx, Namespace, im.branch)
	if err != nil {
		return err
	}
	im.head = head.ID
	if head.ID == "" {
		return nil
	}
	return im.whole(ctx, head.ID)
}

// whole reads the state of a baseline whole, the links to a node that changed since included (they are the nodes'
// all the same: a checkout carries them).
func (im *Importer) whole(ctx context.Context, id domain.BaselineID) error {
	nodes, links, err := im.Graph.BaselineGraph(ctx, id)
	if err != nil {
		return err
	}
	suspect, err := im.Graph.SuspectLinks(ctx, id)
	if err != nil {
		return err
	}
	im.ix.apply(im.Name, nodes, append(links, suspect...))
	return nil
}

// follow brings the index to the head a landing left: the delta from the previous head, the whole state when the
// delta is too large to be read.
func (im *Importer) follow(ctx context.Context, to domain.BaselineID) error {
	if im.head != "" {
		d, err := im.Graph.BaselineDelta(ctx, im.head, to)
		if err == nil {
			im.ix.apply(im.Name, d.Nodes, d.Links)
			im.head = to
			return nil
		} else if !errors.Is(err, graphapi.ErrInvalid) {
			return err
		}
	}
	if err := im.whole(ctx, to); err != nil {
		return err
	}
	im.head = to
	return nil
}

// mine tells the keys of the repository named: its root, its entries and symbols (<repo>:...), its packages
// (<repo>@...).
func mine(repo, key string) bool {
	return key == repo || strings.HasPrefix(key, repo+":") || strings.HasPrefix(key, repo+"@")
}

// apply sets the nodes of the repository named, with their outgoing links (all of them: a node read is read whole).
func (ix *index) apply(repo string, nodes []domain.Node, links []domain.Link) {
	for _, n := range nodes {
		if !mine(repo, n.Key) {
			continue
		}
		e := ix.byKey[n.Key]
		if e == nil {
			e = &entry{}
			ix.byKey[n.Key] = e
		}
		for lid, l := range e.links {
			delete(ix.into[l.to], lid)
		}
		if e.sym != nil {
			delete(ix.fileSyms[e.sym.file], n.Key)
		}
		*e = entry{id: n.ID, typ: n.Type, ver: n.Version, removed: n.State == StateRemoved}
		ix.keyOf[n.ID] = n.Key
		str := func(k string) string { s, _ := n.Properties[k].(string); return s }
		switch {
		case n.Key == repo:
			ix.commit, e.sum = str("commit"), entrySum(n.Type, n.Properties)
		case n.Type == TypeDirectory:
			e.sum = entrySum(n.Type, n.Properties)
		case isSymbolType(n.Type):
			e.sum = fingerprint(n.Properties)
			kind := str("kind")
			file := n.Key
			if i := strings.LastIndex(n.Key, "#"+kind+":"); i >= 0 {
				file = n.Key[:i]
			}
			e.sym = &symInfo{key: n.Key, kind: kind, name: str("name"), container: str("container"), file: file}
			if !e.removed {
				if ix.fileSyms[file] == nil {
					ix.fileSyms[file] = map[string]bool{}
				}
				ix.fileSyms[file][n.Key] = true
			}
		case n.Type == TypePackage:
			e.sum, e.name = pkgFingerprint(n.Properties), str("name")
		case n.Type == TypeFile:
			e.blob, e.sum = str("blob"), entrySum(n.Type, n.Properties)
			if p := str("path"); path.Base(p) == "go.mod" {
				if e.removed {
					delete(ix.gomods, dirOf(p))
				} else {
					ix.gomods[dirOf(p)] = e.blob
				}
			}
		}
	}
	for _, l := range links {
		k, ok := ix.keyOf[l.From.ID]
		if !ok {
			continue
		}
		e := ix.byKey[k]
		if e.links == nil {
			e.links, e.out = map[domain.LinkID]link{}, map[linkKey]domain.LinkID{}
		}
		e.links[l.ID] = link{typ: l.Type, to: l.To.ID, ver: l.To.Version}
		e.out[linkKey{l.Type, l.To.ID}] = l.ID
		if ix.into[l.To.ID] == nil {
			ix.into[l.To.ID] = map[domain.LinkID]domain.NodeID{}
		}
		ix.into[l.To.ID][l.ID] = l.From.ID
	}
}

// ---- one commit, one change ----

// op is what a commit does to one node.
type op struct {
	key, typ string
	// parent is the key of the file defining a symbol (a directory's entries find theirs by their path; a package
	// has none).
	parent string
	pkg    bool
	depth  int
	props  map[string]any // nil: only its links change
	remove bool
	cur    *entry // the node held, nil for a creation
	add    []want
	drop   []domain.LinkID
	impact domain.ChangeImpactID
	ref    domain.NodeRef // its draft, once its operation ran
}

// Key gives the key of an entry of a repository: the repository's name for its root, <repo>:<path>/ for a directory
// and <repo>:<path> for a file (a path may be a file at a commit and a directory at another: two nodes).
func Key(repo, p string, dir bool) string {
	switch {
	case p == "":
		return repo
	case dir:
		return repo + ":" + p + "/"
	}
	return repo + ":" + p
}

// abandonWait bounds the abandon of the change of a commit that failed or was cancelled.
const abandonWait = 30 * time.Second

func (im *Importer) commit(ctx context.Context, c Commit) error {
	if im.OnBegin != nil {
		im.OnBegin(im.stats, c)
	}
	ops, err := im.plan(ctx, c)
	if err != nil {
		return fmt.Errorf("commit %s: %w", short(c.Hash), err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := im.change(ctx, c, ops)
	if err != nil {
		return fmt.Errorf("commit %s: %w", short(c.Hash), err)
	}
	if err := im.follow(ctx, b.ID); err != nil {
		return fmt.Errorf("commit %s: %w", short(c.Hash), err)
	}
	im.stats.Commits++
	im.stats.Last = c.Hash
	im.report(false)
	if im.OnCommit != nil {
		im.OnCommit(im.stats)
	}
	return nil
}

// change writes the change of a commit, by itself or through its sub-changes, and applies it; a change that fails is
// abandoned, with its sub-change at work.
func (im *Importer) change(ctx context.Context, c Commit, ops []*op) (domain.Baseline, error) {
	ch, err := im.Graph.CreateChange(ctx, graphapi.NewChange{Namespace: Namespace, Title: title(c), Intent: capText(c.Message, maxIntent),
		Data: changeData(im.Name, im.gitRef, c), Branch: im.branch, OwnBranch: true, ProjectID: im.ProjectID, OwnerOrg: im.OwnerOrg})
	if err != nil {
		return domain.Baseline{}, err
	}
	var b domain.Baseline
	var sub domain.ChangeID
	batch := im.Batch
	if batch <= 0 {
		batch = defaultBatch
	}
	if len(ops) <= batch {
		b, err = im.write(ctx, ch.ID, c, ops, nil)
	} else {
		done := map[string]domain.NodeRef{}
		units := im.units(ops)
		for i, u := range units {
			if err = ctx.Err(); err != nil {
				break
			}
			if im.OnPart != nil {
				im.OnPart(i+1, len(units), u.name)
			}
			var s domain.Change
			s, err = im.Graph.CreateChange(ctx, graphapi.NewChange{Namespace: Namespace, Title: capText(fmt.Sprintf("%s (%d/%d): %s", title(c), i+1, len(units), u.name), maxTitle),
				ParentID: ch.ID, ProjectID: im.ProjectID, OwnerOrg: im.OwnerOrg})
			if err != nil {
				break
			}
			sub = s.ID
			if _, err = im.write(ctx, sub, c, u.ops, done); err != nil {
				break
			}
			// the versions the sub-change wrote, which the next ones link to
			if s, err = im.Graph.Change(ctx, sub); err != nil {
				break
			}
			sub = ""
			for _, cn := range s.Nodes {
				if cn.Committed != nil {
					done[cn.Key] = *cn.Committed
				}
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			b, err = im.land(ctx, ch.ID)
		}
	}
	if err != nil {
		abandoned := domain.ChangeAbandoned
		// abandoned even when the run is cancelled: an open change would hold the keys it committed
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abandonWait)
		defer cancel()
		for _, id := range []domain.ChangeID{sub, ch.ID} {
			if id == "" {
				continue
			}
			if _, aerr := im.Graph.UpdateChange(actx, id, graphapi.ChangePatch{Status: &abandoned}); aerr != nil {
				err = errors.Join(err, aerr)
			}
		}
		return b, err
	}
	return b, nil
}

const defaultBatch = 1000

// unit is the operations of a sub-change of a commit.
type unit struct {
	name  string
	ops   []*op
	files int // the units packed with the first one
}

// units groups the operations of a commit by file (a symbol goes with its file): the packages first, then each file
// after those whose nodes it links to, the files linking to each other together (a strongly connected component), the
// directories and the root last; small files that follow each other share a sub-change.
func (im *Importer) units(ops []*op) []unit {
	repo := im.Name
	unitOf := func(o *op) string {
		switch {
		case o.pkg:
			return "\x00packages"
		case isSymbolType(o.typ):
			return o.parent
		case o.typ == TypeFile:
			return o.key
		}
		return "\xfftree"
	}
	byUnit := map[string][]*op{}
	opUnit := map[string]string{}
	for _, o := range ops {
		u := unitOf(o)
		byUnit[u] = append(byUnit[u], o)
		opUnit[o.key] = u
	}
	// what each unit links to: the links it adds, and those its nodes hold already (renewed when written)
	deps := map[string]map[string]bool{}
	dep := func(u, k string) {
		if v, ok := opUnit[k]; ok && v != u {
			if deps[u] == nil {
				deps[u] = map[string]bool{}
			}
			deps[u][v] = true
		}
	}
	for _, o := range ops {
		u := opUnit[o.key]
		for _, w := range o.add {
			dep(u, w.key)
		}
		if o.cur != nil {
			for _, l := range o.cur.links {
				dep(u, im.ix.keyOf[l.to])
			}
		}
	}
	tree := "\xfftree"
	if _, ok := byUnit[tree]; ok {
		for u := range byUnit {
			if u != tree {
				dep(tree, byUnit[u][0].key)
			}
		}
	}
	// Tarjan: a component comes out after every component it reaches, so after what it links to
	names := slices.Sorted(maps.Keys(byUnit))
	index, low, on := map[string]int{}, map[string]int{}, map[string]bool{}
	var stack []string
	var out []unit
	var visit func(u string)
	visit = func(u string) {
		index[u], low[u] = len(index), len(index)
		stack = append(stack, u)
		on[u] = true
		for _, v := range slices.Sorted(maps.Keys(deps[u])) {
			if _, seen := index[v]; !seen {
				visit(v)
				low[u] = min(low[u], low[v])
			} else if on[v] {
				low[u] = min(low[u], index[v])
			}
		}
		if low[u] != index[u] {
			return
		}
		var comp []string
		for {
			v := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			on[v] = false
			comp = append(comp, v)
			if v == u {
				break
			}
		}
		slices.Sort(comp)
		var us unit
		for _, v := range comp {
			us.ops = append(us.ops, byUnit[v]...)
		}
		us.name = unitName(repo, comp)
		slices.SortStableFunc(us.ops, byRank)
		out = append(out, us)
	}
	for _, u := range names {
		if _, seen := index[u]; !seen {
			visit(u)
		}
	}
	// the small ones that follow each other go together, up to half a batch: a merge into the change of the commit
	// costs in proportion to what it holds
	limit := im.Batch
	if limit <= 0 {
		limit = defaultBatch
	}
	limit /= 2
	var packed []unit
	for _, u := range out {
		if n := len(packed); n > 0 && len(packed[n-1].ops)+len(u.ops) <= limit {
			last := &packed[n-1]
			last.ops = append(last.ops, u.ops...)
			slices.SortStableFunc(last.ops, byRank)
			last.files++
			continue
		}
		packed = append(packed, u)
	}
	for i := range packed {
		if packed[i].files > 0 {
			packed[i].name = fmt.Sprintf("%s and %d more", packed[i].name, packed[i].files)
		}
	}
	return packed
}

func unitName(repo string, comp []string) string {
	name := func(u string) string {
		switch u {
		case "\x00packages":
			return "packages"
		case "\xfftree":
			return "directories"
		}
		return strings.TrimPrefix(u, repo+":")
	}
	if len(comp) == 1 {
		return name(comp[0])
	}
	return fmt.Sprintf("%s and %d other files", name(comp[0]), len(comp)-1)
}

// plan turns the raw diff of a commit into the operations on the nodes, deepest first: an entry's draft exists before
// the directory linking to it.
func (im *Importer) plan(ctx context.Context, c Commit) ([]*op, error) {
	ops := map[string]*op{}
	get := func(p string, dir bool) *op {
		k := Key(im.Name, p, dir)
		o := ops[k]
		if o == nil {
			o = &op{key: k, typ: TypeFile, cur: im.ix.byKey[k]}
			switch {
			case p == "":
				o.typ = TypeRepo
			case dir:
				o.typ = TypeDirectory
			}
			if p != "" {
				o.depth = strings.Count(p, "/") + 1
			}
			ops[k] = o
		}
		return o
	}
	root := get("", true)
	root.props = map[string]any{"name": im.Name, "path": "", "tree": c.Tree, "commit": c.Hash, "url": im.Source, "ref": im.Rev}
	for _, e := range c.Entries {
		oldDir, newDir := IsTree(e.OldMode), IsTree(e.NewMode)
		had, has := e.OldMode != modeNone && e.Status != 'A', e.NewMode != modeNone && e.Status != 'D'
		if had && (!has || oldDir != newDir) {
			if o := get(e.Path, oldDir); o.cur != nil && !o.cur.removed {
				o.remove = true
			} else if o.props == nil {
				delete(ops, o.key) // removing what the graph does not hold
			}
		}
		if has {
			props, err := im.props(e, newDir)
			if err != nil {
				return nil, err
			}
			o := get(e.Path, newDir)
			o.props, o.remove = props, false
		}
	}
	if im.Symbols != nil {
		if err := im.planCode(ctx, ops); err != nil {
			return nil, err
		}
	}
	// the directories gain the entries they did not hold and lose the ones removed, the files their symbols; a node
	// that changes gets its parent a new version, up to the root (a version of a directory is a git tree)
	parentOf := func(o *op) (*op, bool) {
		var p string
		dir := true
		switch {
		case o.pkg || o.key == im.Name:
			return nil, false
		case o.parent != "":
			if f := ops[o.parent]; f != nil {
				return f, false
			}
			p, dir = strings.TrimPrefix(o.parent, im.Name+":"), false
		default:
			p = dirOf(strings.TrimSuffix(strings.TrimPrefix(o.key, im.Name+":"), "/"))
		}
		_, had := ops[Key(im.Name, p, dir)]
		return get(p, dir), !had
	}
	queue := slices.Collect(maps.Values(ops))
	for len(queue) > 0 {
		o := queue[0]
		queue = queue[1:]
		parent, made := parentOf(o)
		if parent == nil {
			continue
		}
		if made {
			queue = append(queue, parent)
		}
		typ := LinkContains
		if parent.typ == TypeFile {
			typ = LinkDefines
		}
		switch {
		case o.remove:
			if parent.remove || parent.cur == nil {
				continue
			}
			if l := parent.cur.linkTo(typ, o.cur.id); l != "" {
				parent.drop = append(parent.drop, l)
			}
		case o.cur == nil || o.cur.removed || parent.cur == nil || parent.cur.removed:
			parent.add = append(parent.add, want{typ, o.key})
		case parent.cur.linkTo(typ, o.cur.id) == "":
			parent.add = append(parent.add, want{typ, o.key}) // a link the directory lost: the graph and git disagreed
		}
	}
	for _, o := range ops {
		if o.remove {
			o.add = nil
			if o.cur.links != nil {
				for l := range o.cur.links {
					o.drop = append(o.drop, l)
				}
			}
		}
		if !o.remove && (o.cur == nil || o.cur.removed) && o.props == nil {
			return nil, fmt.Errorf("%s is linked but git gives none of its attributes", o.key)
		}
		slices.SortFunc(o.add, func(a, b want) int { return strings.Compare(a.typ+"\x00"+a.key, b.typ+"\x00"+b.key) })
		o.add = slices.Compact(o.add)
		slices.Sort(o.drop)
		o.drop = slices.Compact(o.drop)
		// what the graph holds already (a commit written in part before a cancel) is left alone
		if o.cur != nil && !o.cur.removed && !o.remove {
			if o.props != nil && o.typ != TypePackage && !isSymbolType(o.typ) && entrySum(o.typ, o.props) == o.cur.sum {
				o.props = nil
			}
			if o.props == nil && len(o.add)+len(o.drop) == 0 {
				delete(ops, o.key)
			}
		}
	}
	out := slices.Collect(maps.Values(ops))
	slices.SortFunc(out, byRank)
	return out, nil
}

func byRank(a, b *op) int {
	if ra, rb := rank(a), rank(b); ra != rb {
		return ra - rb
	}
	if a.depth != b.depth {
		return b.depth - a.depth
	}
	return strings.Compare(a.key, b.key)
}

// rank orders the operations of a change: the packages, the symbols, then the files and directories, deepest first,
// the root last (a creation carries the links to the nodes written before it).
func rank(o *op) int {
	switch {
	case o.pkg:
		return 0
	case isSymbolType(o.typ):
		return 1
	}
	return 2
}

// entrySum is the attributes of a file, a directory or the repository that tell a changed one (the others derive from
// them).
func entrySum(typ string, p map[string]any) string {
	switch typ {
	case TypeFile:
		return sumOf(p, "name", "path", "blob", "mode")
	case TypeDirectory:
		return sumOf(p, "name", "path", "tree")
	}
	return sumOf(p, "name", "path", "tree", "commit", "url", "ref")
}

func (im *Importer) tally(o *op) {
	switch {
	case o.pkg:
		im.stats.Packages++
	case isSymbolType(o.typ) || (o.cur != nil && o.cur.sym != nil):
		im.stats.Symbols++
	}
}

// fingerprint is the attributes of a symbol, compared to tell a changed one (its code is in its digest; kept or not,
// it is the same symbol).
func fingerprint(p map[string]any) string {
	return sumOf(p, "name", "kind", "container", "language", "signature", "digest")
}

// pkgFingerprint is the attributes of a package.
func pkgFingerprint(p map[string]any) string { return sumOf(p, "name", "path", "language") }

func sumOf(p map[string]any, keys ...string) string {
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%v\x00", p[k])
	}
	return b.String()
}

func (im *Importer) props(e Entry, dir bool) (map[string]any, error) {
	name := path.Base(e.Path)
	if dir {
		return map[string]any{"name": name, "path": e.Path, "tree": e.NewID}, nil
	}
	p := map[string]any{"name": name, "path": e.Path, "blob": e.NewID, "mode": e.NewMode}
	if ext := path.Ext(name); len(ext) > 1 && ext != name {
		p["extension"] = strings.ToLower(ext[1:])
	}
	if e.NewMode != ModeGitlink {
		n, err := im.sizes.Size(e.NewID)
		if err != nil {
			return nil, err
		}
		p["size"] = n
	}
	return p, nil
}

// write runs the operations of a commit on its change, accepts them in one review and applies the change: the nodes
// first (a creation with the links whose targets have their draft already), then the other links.
// A node written renews the links it holds to an older version of their target than the head, or than the version an
// earlier sub-change of the commit wrote (done).
func (im *Importer) write(ctx context.Context, id domain.ChangeID, c Commit, ops []*op, done map[string]domain.NodeRef) (domain.Baseline, error) {
	byKey := map[string]*op{}
	for _, o := range ops {
		byKey[o.key] = o
	}
	why := short(c.Hash) + ": " + capText(c.Subject(), 200)
	// target is the node a link goes to: its draft when the change writes it, else its version at the head
	target := func(k string) (domain.NodeRef, bool) {
		if t := byKey[k]; t != nil {
			return t.ref, t.ref.ID != ""
		}
		if r, ok := done[k]; ok {
			return r, true
		}
		if e := im.ix.byKey[k]; e != nil {
			return domain.NodeRef{ID: e.id, Version: e.ver}, true
		}
		return domain.NodeRef{}, false
	}
	later := map[*op][]want{}
	for _, o := range ops {
		// a cancel stops the change between two writes, whether the graph refuses the next or not
		if err := ctx.Err(); err != nil {
			return domain.Baseline{}, err
		}
		switch {
		case o.cur == nil:
			var links []graphapi.LinkWrite
			for _, w := range o.add {
				if to, ok := target(w.key); ok {
					links = append(links, graphapi.LinkWrite{Type: w.typ, To: to})
				} else {
					later[o] = append(later[o], w)
				}
			}
			cn, err := im.Graph.ImpactNodeCreate(ctx, id, graphapi.NodeCreate{Key: o.key, Type: o.typ, Properties: o.props, Links: links, Rationale: why, ProducedBy: im.By})
			if err != nil {
				return domain.Baseline{}, fmt.Errorf("%s: %w", o.key, err)
			}
			if cn.Post == nil {
				return domain.Baseline{}, fmt.Errorf("%s: created without a draft", o.key)
			}
			o.impact, o.ref = cn.ID, *cn.Post
			im.stats.Created++
			im.tally(o)
			continue
		case o.remove:
			cn, err := im.Graph.ImpactNodeTransition(ctx, id, graphapi.NodeTransition{NodeCheckout: graphapi.NodeCheckout{Node: o.cur.id, Rationale: why, ProducedBy: im.By}, To: StateRemoved})
			if err != nil {
				return domain.Baseline{}, fmt.Errorf("%s: %w", o.key, err)
			}
			o.impact = cn.ID
			im.stats.Removed++
			im.tally(o)
		default:
			cn, err := im.Graph.ImpactNodeCheckout(ctx, id, graphapi.NodeCheckout{Node: o.cur.id, Rationale: why, ProducedBy: im.By})
			if err != nil {
				return domain.Baseline{}, fmt.Errorf("%s: %w", o.key, err)
			}
			o.impact = cn.ID
			if o.props != nil {
				if _, err := im.Graph.ImpactNodeUpdate(ctx, id, o.impact, graphapi.NodeUpdate{Properties: o.props}); err != nil {
					return domain.Baseline{}, fmt.Errorf("%s: %w", o.key, err)
				}
			}
			if o.cur.removed {
				if _, err := im.Graph.ImpactNodeTransition(ctx, id, graphapi.NodeTransition{NodeCheckout: graphapi.NodeCheckout{Impact: o.impact, ProducedBy: im.By}, To: StatePresent}); err != nil {
					return domain.Baseline{}, fmt.Errorf("%s: %w", o.key, err)
				}
			}
			im.stats.Modified++
			im.tally(o)
		}
		o.ref = domain.DraftRef(o.cur.id)
		drop := slices.Clone(o.drop)
		later[o] = slices.Clone(o.add)
		if !o.remove {
			drop, later[o] = im.renew(o, byKey, done, drop, later[o])
		}
		for _, l := range drop {
			if err := im.Graph.ImpactLinkDelete(ctx, id, l, "", ""); err != nil {
				return domain.Baseline{}, fmt.Errorf("%s: %w", o.key, err)
			}
		}
	}
	for _, o := range ops {
		for _, w := range later[o] {
			if err := ctx.Err(); err != nil {
				return domain.Baseline{}, err
			}
			to, ok := target(w.key)
			if !ok {
				return domain.Baseline{}, fmt.Errorf("%s: no node %s to link to", o.key, w.key)
			}
			if _, err := im.Graph.ImpactLinkCreate(ctx, id, o.impact, graphapi.LinkWrite{Type: w.typ, To: to}, "", ""); err != nil {
				return domain.Baseline{}, fmt.Errorf("%s: %w", o.key, err)
			}
		}
	}
	accept := domain.ReviewBatch{By: im.By, Verdicts: make([]domain.ImpactVerdict, 0, len(ops))}
	for _, o := range ops {
		accept.Verdicts = append(accept.Verdicts, domain.ImpactVerdict{Impact: o.impact, Status: domain.ReviewAccepted, Comment: why})
	}
	if err := ctx.Err(); err != nil {
		return domain.Baseline{}, err
	}
	if _, err := im.Graph.ImpactNodeReviewBatch(ctx, id, accept); err != nil {
		return domain.Baseline{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Baseline{}, err
	}
	return im.land(ctx, id)
}

// land commits a change and closes it, merged into the branch it was forked from (main, or the branch of the change of its
// commit for a sub-change): the baseline it leaves on main, by its header (empty for a sub-change).
func (im *Importer) land(ctx context.Context, id domain.ChangeID) (domain.Baseline, error) {
	if _, err := im.Graph.CommitChange(ctx, id, ""); err != nil && !errors.Is(err, graphapi.ErrNothingToCommit) {
		return domain.Baseline{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Baseline{}, err
	}
	c, err := im.Graph.CloseChange(ctx, id)
	if err != nil {
		return domain.Baseline{}, err
	}
	if c.ParentID == "" && (c.Status != domain.ChangeApplied || c.ResultBaselineID == "") {
		return domain.Baseline{}, fmt.Errorf("change %s left %s: the head moved while it was imported: %w", id, c.Status, graphapi.ErrConflict)
	}
	return domain.Baseline{ID: c.ResultBaselineID}, nil
}

// renew adds to the links a node written drops and adds those it holds to an older version of their target than the
// one it will hold: the head, the version an earlier sub-change of the commit wrote, or the draft of the change (the
// landing follows a link to the version the draft was checked out from by itself).
func (im *Importer) renew(o *op, byKey map[string]*op, done map[string]domain.NodeRef, drop []domain.LinkID, add []want) ([]domain.LinkID, []want) {
	dropped := map[domain.LinkID]bool{}
	for _, l := range drop {
		dropped[l] = true
	}
	for _, lid := range slices.Sorted(maps.Keys(o.cur.links)) {
		l := o.cur.links[lid]
		if dropped[lid] {
			continue
		}
		k := im.ix.keyOf[l.to]
		if t := byKey[k]; t != nil {
			if t.cur == nil || t.cur.ver == l.ver {
				continue
			}
		} else if r, ok := done[k]; ok {
			if r.Version == l.ver {
				continue
			}
		} else if e := im.ix.byKey[k]; e == nil || e.ver == l.ver {
			continue
		}
		drop = append(drop, lid)
		add = append(add, want{l.typ, k})
	}
	return drop, add
}

// ---- the change of a commit ----

const (
	maxTitle  = 200
	maxIntent = 8000
)

func title(c Commit) string {
	if s := c.Subject(); s != "" {
		return capText(s, maxTitle)
	}
	return "commit " + short(c.Hash)
}

// changeData is what the change keeps of its commit (Change.Data, free-form).
func changeData(repo, branch string, c Commit) map[string]any {
	parents := make([]any, len(c.Parents))
	for i, p := range c.Parents {
		parents[i] = p
	}
	git := map[string]any{
		"repository": repo, "commit": c.Hash, "parents": parents, "tree": c.Tree,
		"author": c.Author.Name, "authorEmail": c.Author.Email, "authorDate": c.Author.When.Format(time.RFC3339),
		"committer": c.Committer.Name, "committerEmail": c.Committer.Email, "committerDate": c.Committer.When.Format(time.RFC3339),
	}
	if branch != "" {
		git["branch"] = branch
	}
	return map[string]any{"git": git}
}

// capText cuts a text to at most n bytes, on a rune boundary.
func capText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
