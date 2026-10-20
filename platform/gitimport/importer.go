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
	LinkContains  = "code@contains"
	LinkDefines   = "code@defines"
	StatePresent  = "present"
	StateRemoved  = "removed"
)

// Port is what the importer asks of the graph: open a change, give it its node operations, review and apply it, and
// read the heads it follows (*graph.Graph and graphclient.Client).
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
	Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error)
	BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
	BaselineDelta(ctx context.Context, from, to domain.BaselineID) (graphapi.BaselineDelta, error)
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
	// Rev is the revision followed (default HEAD).
	Rev string
	// Base, for a first import, starts at that revision: its whole tree is the first change, then the commits after it
	// (the history of the Linux kernel before a release left out). Empty: from the root commit.
	Base string
	// Max bounds the number of commits of this run (0: no bound).
	Max int
	// ProjectID is the project the changes act in (required, ADR 0091); OwnerOrg the unit holding them (empty: the root).
	ProjectID, OwnerOrg string
	// By is who accepts the changes (default "gitimport").
	By string
	// Symbols, when set, reads the declarations of the source files a commit adds or changes (NewTreeSitter): each a
	// Symbol node its file defines. MaxSymbolBytes leaves the larger files out (default 1 MiB).
	Symbols        Extractor
	MaxSymbolBytes int64
	// Progress, when set, receives a line every ProgressEvery commits (default 100) and at the end.
	Progress      io.Writer
	ProgressEvery int

	ix    index
	head  domain.BaselineID
	sizes *Sizes
	blobs *Blobs
	stats Stats
	start time.Time
}

// Stats is what a run did.
type Stats struct {
	Commits                    int
	Created, Modified, Removed int
	// Symbols counts the symbols created, modified or removed (among the nodes counted above).
	Symbols int
	// Last is the last commit imported.
	Last string
}

// Run imports the commits not imported yet and returns what it did.
func (im *Importer) Run(ctx context.Context) (Stats, error) {
	if im.Name == "" || strings.ContainsAny(im.Name, ":/") {
		return im.stats, fmt.Errorf("gitimport: the repository needs a name without : nor /, got %q", im.Name)
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
	if from == "" && im.Base != "" {
		c, err := Tree(ctx, im.Dir, im.Base)
		if err != nil {
			return im.stats, err
		}
		if err := im.commit(ctx, c); err != nil {
			return im.stats, err
		}
		from = c.Hash
	}
	if from == rev {
		im.report(true)
		return im.stats, nil
	}
	max := im.Max
	if max > 0 && im.stats.Commits >= max {
		im.report(true)
		return im.stats, nil
	} else if max > 0 {
		max -= im.stats.Commits
	}
	err = Log(ctx, im.Dir, from, rev, max, func(c Commit) error { return im.commit(ctx, c) })
	im.report(true)
	return im.stats, err
}

func (im *Importer) report(final bool) {
	if im.Progress == nil || (!final && im.stats.Commits%im.ProgressEvery != 0) {
		return
	}
	el := time.Since(im.start)
	rate := float64(im.stats.Commits) / el.Seconds()
	fmt.Fprintf(im.Progress, "%s: %d commits (%.1f/s), %d nodes created, %d modified, %d removed (%d symbols), at %s\n",
		im.Name, im.stats.Commits, rate, im.stats.Created, im.stats.Modified, im.stats.Removed, im.stats.Symbols, short(im.stats.Last))
}

// ---- the repository as the graph holds it ----

// entry is a node of the repository at the head.
type entry struct {
	id      domain.NodeID
	removed bool
	// links are the entries a directory holds, the symbols a file defines: node → link.
	links map[domain.NodeID]domain.LinkID
	// sum is the fingerprint of a symbol's attributes (git says when a directory or a file changed, not of a symbol).
	sum string
}

// index is the repository at the head of the namespace, by key: read once whole, then followed commit by commit by the
// delta of each landing.
type index struct {
	byKey  map[string]*entry
	keyOf  map[domain.NodeID]string
	commit string // the last commit imported (the repository node's)
}

func (im *Importer) load(ctx context.Context) error {
	im.ix = index{byKey: map[string]*entry{}, keyOf: map[domain.NodeID]string{}}
	head, err := im.Graph.BranchHead(ctx, Namespace, domain.MainBranch)
	if err != nil {
		return err
	}
	im.head = head.ID
	if head.ID == "" {
		return nil
	}
	nodes, links, err := im.Graph.BaselineGraph(ctx, head.ID)
	if err != nil {
		return err
	}
	im.ix.apply(im.Name, nodes, links)
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
	nodes, links, err := im.Graph.BaselineGraph(ctx, to)
	if err != nil {
		return err
	}
	im.ix.apply(im.Name, nodes, links)
	im.head = to
	return nil
}

// apply sets the nodes of the repository named, with their outgoing links (all of them: a node read is read whole).
func (ix *index) apply(repo string, nodes []domain.Node, links []domain.Link) {
	mine := func(key string) bool { return key == repo || strings.HasPrefix(key, repo+":") }
	for _, n := range nodes {
		if !mine(n.Key) {
			continue
		}
		e := ix.byKey[n.Key]
		if e == nil {
			e = &entry{}
			ix.byKey[n.Key] = e
		}
		e.id, e.removed, e.links, e.sum = n.ID, n.State == StateRemoved, nil, ""
		if n.Type == TypeSymbol {
			e.sum = fingerprint(n.Properties)
		}
		ix.keyOf[n.ID] = n.Key
		if n.Key == repo {
			ix.commit, _ = n.Properties["commit"].(string)
		}
	}
	for _, l := range links {
		if l.Type != LinkContains && l.Type != LinkDefines {
			continue
		}
		k, ok := ix.keyOf[l.From.ID]
		if !ok {
			continue
		}
		e := ix.byKey[k]
		if e.links == nil {
			e.links = map[domain.NodeID]domain.LinkID{}
		}
		e.links[l.To.ID] = l.ID
	}
}

// ---- one commit, one change ----

// op is what a commit does to one node.
type op struct {
	key, typ string
	// parent is the key of the file defining a symbol (a directory's entries find theirs by their path).
	parent string
	depth  int
	props  map[string]any // nil: only its entries change
	remove bool
	cur    *entry // the node held, nil for a creation
	add    []string
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

func (im *Importer) commit(ctx context.Context, c Commit) error {
	ops, err := im.plan(c)
	if err != nil {
		return fmt.Errorf("commit %s: %w", short(c.Hash), err)
	}
	ch, err := im.Graph.CreateChange(ctx, graphapi.NewChange{Namespace: Namespace, Title: title(c), Intent: capText(c.Message, maxIntent),
		Data: changeData(im.Name, c), OwnBranch: true, ProjectID: im.ProjectID, OwnerOrg: im.OwnerOrg})
	if err != nil {
		return fmt.Errorf("commit %s: %w", short(c.Hash), err)
	}
	b, err := im.write(ctx, ch.ID, c, ops)
	if err != nil {
		abandoned := domain.ChangeAbandoned
		if _, aerr := im.Graph.UpdateChange(ctx, ch.ID, graphapi.ChangePatch{Status: &abandoned}); aerr != nil {
			err = errors.Join(err, aerr)
		}
		return fmt.Errorf("commit %s: %w", short(c.Hash), err)
	}
	if err := im.follow(ctx, b.ID); err != nil {
		return fmt.Errorf("commit %s: %w", short(c.Hash), err)
	}
	im.stats.Commits++
	im.stats.Last = c.Hash
	im.report(false)
	return nil
}

// plan turns the raw diff of a commit into the operations on the nodes, deepest first: an entry's draft exists before
// the directory linking to it.
func (im *Importer) plan(c Commit) ([]*op, error) {
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
		for _, o := range slices.Collect(maps.Values(ops)) {
			if o.typ == TypeFile {
				if err := im.planSymbols(ops, o); err != nil {
					return nil, err
				}
			}
		}
	}
	// the directories gain the entries they did not hold and lose the ones removed, the files their symbols
	parentOf := func(o *op) *op {
		if o.parent != "" {
			return ops[o.parent]
		}
		if o.key == im.Name {
			return nil
		}
		p := strings.TrimSuffix(strings.TrimPrefix(o.key, im.Name+":"), "/")
		d := path.Dir(p)
		if d == "." {
			d = ""
		}
		return get(d, true)
	}
	for _, o := range slices.Collect(maps.Values(ops)) {
		parent := parentOf(o)
		if parent == nil {
			continue
		}
		switch {
		case o.remove:
			if parent.remove || parent.cur == nil {
				continue
			}
			if l, ok := parent.cur.links[o.cur.id]; ok {
				parent.drop = append(parent.drop, l)
			}
		case o.cur == nil || o.cur.removed || parent.cur == nil || parent.cur.removed:
			parent.add = append(parent.add, o.key)
		case parent.cur.links[o.cur.id] == "":
			parent.add = append(parent.add, o.key) // a link the directory lost: the graph and git disagreed
		}
	}
	for _, o := range ops {
		if o.remove && o.cur.links != nil {
			for _, l := range o.cur.links {
				o.drop = append(o.drop, l)
			}
		}
		if !o.remove && (o.cur == nil || o.cur.removed) && o.props == nil {
			return nil, fmt.Errorf("%s is linked but git gives none of its attributes", o.key)
		}
		slices.Sort(o.add)
		slices.Sort(o.drop)
	}
	out := slices.Collect(maps.Values(ops))
	slices.SortFunc(out, func(a, b *op) int {
		if a.depth != b.depth {
			return b.depth - a.depth
		}
		return strings.Compare(a.key, b.key)
	})
	return out, nil
}

// planSymbols adds to the operations of a commit those on the symbols of a file it adds, changes or removes: the
// declarations Tree-sitter reads in its new content against the symbols the file defines at the head.
func (im *Importer) planSymbols(ops map[string]*op, f *op) error {
	held := map[string]bool{} // the symbols the file defines at the head
	if f.cur != nil && !f.cur.removed {
		for id := range f.cur.links {
			if k, ok := im.ix.keyOf[id]; ok && !im.ix.byKey[k].removed {
				held[k] = true
			}
		}
	}
	sym := func(k string) *op {
		o := &op{key: k, typ: TypeSymbol, parent: f.key, depth: f.depth + 1, cur: im.ix.byKey[k]}
		ops[k] = o
		return o
	}
	removeHeld := func() {
		for k := range held {
			sym(k).remove = true
		}
	}
	if f.remove {
		removeHeld()
		return nil
	}
	if f.props == nil {
		return nil
	}
	name, _ := f.props["name"].(string)
	mode, _ := f.props["mode"].(string)
	size, _ := f.props["size"].(int64)
	lang := im.Symbols.Language(name)
	if lang == "" || (mode != "100644" && mode != "100755") || size > im.MaxSymbolBytes {
		removeHeld()
		return nil
	}
	blob, _ := f.props["blob"].(string)
	src, err := im.blobs.Read(blob)
	if err != nil {
		return err
	}
	syms, err := im.Symbols.Symbols(lang, src)
	if err != nil {
		return fmt.Errorf("%s: %w", f.key, err)
	}
	for i, k := range symbolKeys(f.key, syms) {
		s := syms[i]
		props := map[string]any{"name": s.Name, "kind": s.Kind, "container": s.Container, "language": lang, "signature": s.Signature, "digest": s.Digest}
		delete(held, k)
		o := sym(k)
		if o.cur != nil && !o.cur.removed && o.cur.sum == fingerprint(props) {
			delete(ops, k) // unchanged: the file's new version keeps its link
			continue
		}
		o.props = props
	}
	removeHeld()
	return nil
}

func (im *Importer) countSymbol(o *op) {
	if o.typ == TypeSymbol {
		im.stats.Symbols++
	}
}

// fingerprint is the attributes of a symbol, compared to tell a changed one.
func fingerprint(p map[string]any) string {
	var b strings.Builder
	for _, k := range []string{"name", "kind", "container", "language", "signature", "digest"} {
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

// write runs the operations of a commit on its change, accepts them in one review and applies the change.
func (im *Importer) write(ctx context.Context, id domain.ChangeID, c Commit, ops []*op) (domain.Baseline, error) {
	byKey := map[string]*op{}
	for _, o := range ops {
		byKey[o.key] = o
	}
	why := short(c.Hash) + ": " + capText(c.Subject(), 200)
	linksTo := func(o *op) []graphapi.LinkWrite {
		out := make([]graphapi.LinkWrite, 0, len(o.add))
		for _, k := range o.add {
			typ := LinkContains
			if o.typ == TypeFile {
				typ = LinkDefines
			}
			out = append(out, graphapi.LinkWrite{Type: typ, To: byKey[k].ref})
		}
		return out
	}
	for _, o := range ops {
		switch {
		case o.cur == nil:
			cn, err := im.Graph.ImpactNodeCreate(ctx, id, graphapi.NodeCreate{Key: o.key, Type: o.typ, Properties: o.props, Links: linksTo(o), Rationale: why, ProducedBy: im.By})
			if err != nil {
				return domain.Baseline{}, fmt.Errorf("%s: %w", o.key, err)
			}
			if cn.Post == nil {
				return domain.Baseline{}, fmt.Errorf("%s: created without a draft", o.key)
			}
			o.impact, o.ref = cn.ID, *cn.Post
			im.stats.Created++
			im.countSymbol(o)
			continue
		case o.remove:
			cn, err := im.Graph.ImpactNodeTransition(ctx, id, graphapi.NodeTransition{NodeCheckout: graphapi.NodeCheckout{Node: o.cur.id, Rationale: why, ProducedBy: im.By}, To: StateRemoved})
			if err != nil {
				return domain.Baseline{}, fmt.Errorf("%s: %w", o.key, err)
			}
			o.impact = cn.ID
			im.stats.Removed++
			im.countSymbol(o)
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
			im.countSymbol(o)
		}
		o.ref = domain.DraftRef(o.cur.id)
		for _, l := range o.drop {
			if err := im.Graph.ImpactLinkDelete(ctx, id, l, "", ""); err != nil {
				return domain.Baseline{}, fmt.Errorf("%s: %w", o.key, err)
			}
		}
		for _, l := range linksTo(o) {
			if _, err := im.Graph.ImpactLinkCreate(ctx, id, o.impact, l, "", ""); err != nil {
				return domain.Baseline{}, fmt.Errorf("%s: %w", o.key, err)
			}
		}
	}
	accept := domain.ReviewBatch{By: im.By, Verdicts: make([]domain.ImpactVerdict, 0, len(ops))}
	for _, o := range ops {
		accept.Verdicts = append(accept.Verdicts, domain.ImpactVerdict{Impact: o.impact, Status: domain.ReviewAccepted, Comment: why})
	}
	if _, err := im.Graph.ImpactNodeReviewBatch(ctx, id, accept); err != nil {
		return domain.Baseline{}, err
	}
	return im.Graph.Apply(ctx, id, "")
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
func changeData(repo string, c Commit) map[string]any {
	parents := make([]any, len(c.Parents))
	for i, p := range c.Parents {
		parents[i] = p
	}
	return map[string]any{"git": map[string]any{
		"repository": repo, "commit": c.Hash, "parents": parents, "tree": c.Tree,
		"author": c.Author.Name, "authorEmail": c.Author.Email, "authorDate": c.Author.When.Format(time.RFC3339),
		"committer": c.Committer.Name, "committerEmail": c.Committer.Email, "committerDate": c.Committer.When.Format(time.RFC3339),
	}}
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
