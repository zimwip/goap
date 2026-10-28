package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/operations"
	"github.com/zimwip/goap/lib/platform"
)

// changeTx runs fn in a transaction holding the lock of the tree of change id (ADR 0127): the operations that write a
// change take it before they read what they check, so that it still holds when they write.
func (g *Graph) changeTx(ctx context.Context, id domain.ChangeID, fn func(tx Tx) error) error {
	return g.repo.InTx(ctx, func(tx Tx) error {
		if err := tx.LockChange(ctx, id); err != nil {
			return err
		}
		return fn(tx)
	})
}

// changeOpen loads a change and checks that it still accepts change impacts. It is the start of a write: it takes the
// lock of the change's tree first (ADR 0127).
func changeOpen(ctx context.Context, tx Tx, id domain.ChangeID) (domain.Change, error) {
	if err := tx.LockChange(ctx, id); err != nil {
		return domain.Change{}, err
	}
	read := tx.Change
	if o, ok := tx.(interface {
		openChange(context.Context, domain.ChangeID) (domain.Change, error)
	}); ok {
		read = o.openChange // its impacts and items shared with the view of the change: read, never edited
	}
	c, err := read(ctx, id)
	if err != nil {
		return c, err
	}
	switch c.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned, domain.ChangeCommitted:
		return c, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	return c, nil
}

func findChangeImpact(c domain.Change, id domain.ChangeImpactID) (domain.ChangeImpact, error) {
	all := c.Impacts()
	if i := all.Index(id); i >= 0 {
		return all.At(i), nil
	}
	return domain.ChangeImpact{}, fmt.Errorf("change impact %s of change %s: %w", id, c.ID, ErrNotFound)
}

// ProposeImpact adds change impacts to a change (ADR 0024): each names an existing node (intent modified) and a
// rationale; its pre version must be in the reference commit. The impact has no version until a ImpactNodeCheckout
// writes one (ADR 0076). A new node is created by ImpactNodeCreate, which adds its impact and its draft in one event
// (ADR 0077); it may be planned before (intent created, a key and a type, no draft): what an impact analysis finds the
// change will create, written later by ImpactNodeCreate or ImpactNodeCheckout on the same key, else withdrawn.
//
//goap:operation impact.propose doer
func (g *Graph) ProposeImpact(ctx context.Context, id domain.ChangeID, nodes []domain.ChangeImpact) (out []domain.ChangeImpact, err error) {
	if err := g.allow(ctx, id, operations.ImpactPropose); err != nil {
		return out, err
	}
	for _, cn := range nodes {
		if cn.Intent == domain.IntentCreated && (cn.Key == "" || cn.Type == "") {
			return nil, fmt.Errorf("change impact of %q: a planned creation needs a key and a type: %w", cn.Key, ErrInvalid)
		}
	}
	err = g.repo.InTx(ctx, func(tx Tx) (err error) {
		out, err = g.declareTx(ctx, tx, id, nodes, true)
		return err
	})
	return out, err
}

// declareTx declares change impacts in a transaction (ProposeImpact, and the impact an operation on an existing node
// declares). With propose false it only checks and returns them: the caller records them itself (a creation, whose
// created event carries the impact).
func (g *Graph) declareTx(ctx context.Context, tx Tx, id domain.ChangeID, nodes []domain.ChangeImpact, propose bool) ([]domain.ChangeImpact, error) {
	out := make([]domain.ChangeImpact, 0, len(nodes))
	err := func() error {
		c, err := changeOpen(ctx, tx, id)
		if err != nil {
			return err
		}
		// the reference commit is read for the nodes the impacts name, never whole (ADR 0137)
		b, err := headerOf(ctx, tx, c.ForkCommit)
		if err != nil {
			return err
		}
		var ids []domain.NodeID
		for _, cn := range nodes {
			if cn.Pre != nil {
				ids = append(ids, cn.Pre.ID)
			}
			if propose && cn.Intent == domain.IntentCreated {
				if nid, err := tx.NodeIDByKey(ctx, domain.NamespaceOf(c.Namespace), cn.Key); err == nil {
					ids = append(ids, nid)
				} else if !errors.Is(err, ErrNotFound) {
					return err
				}
			}
		}
		if b.Nodes, err = versionsAt(ctx, tx, b.ID, ids); err != nil {
			return err
		}
		ix, err := g.typesAt(ctx, tx, c.ForkCommit)
		if err != nil {
			return err
		}
		// the impacts a via may name, read only when one names one
		var known map[domain.ChangeImpactID]bool
		isKnown := func(id domain.ChangeImpactID) bool {
			if known == nil {
				known = make(map[domain.ChangeImpactID]bool, c.Impacts().Len()+len(nodes))
				for _, cn := range c.Impacts().All() {
					known[cn.ID] = true
				}
				for _, cn := range nodes {
					if cn.ID != "" {
						known[cn.ID] = true
					}
				}
			}
			return known[id]
		}
		// a node appears once per flow: what the flow sees (the stale change impacts of a relaunched step do not count)
		presByFlow := map[string]*presence{}
		presOf := func(flow string) *presence {
			p, ok := presByFlow[flow]
			if !ok {
				p = &presence{impacts: c.Impacts(), fv: g.newFlowNodes(tx, c, flow)}
				presByFlow[flow] = p
			}
			return p
		}
		batch := slices.Clone(nodes)
		for i := range batch {
			if batch[i].ID == "" {
				batch[i].ID = domain.ChangeImpactID(g.newID())
			}
			batch[i].Flow = c.ResolveFlow(batch[i].Flow) // no flow: the active option (ADR 0032 §6)
			batch[i].Validation = nil                    // a read model, never stored
		}
		inBatch := map[domain.ChangeImpactID]bool{}
		for _, cn := range batch {
			inBatch[cn.ID] = true
		}
		for _, cn := range batch {
			if cn.Post != nil || cn.Committed != nil || len(cn.Reviews) > 0 || cn.Recheck {
				return fmt.Errorf("change impact %s: post, committed and reviews are set by ImpactNodeCreate / ImpactNodeCheckout, ImpactNodeReview and Apply: %w", cn.ID, ErrInvalid)
			}
			if cn.Review == "" {
				cn.Review = domain.ReviewProposed
			}
			if cn.Review != domain.ReviewProposed {
				return fmt.Errorf("change impact %s starts proposed, it is accepted or rejected by ImpactNodeReview: %w", cn.ID, ErrInvalid)
			}
			if cn.Flow != "" {
				if _, ok := c.Flow(cn.Flow); !ok {
					return fmt.Errorf("change impact %s: unknown flow %s: %w", cn.ID, cn.Flow, ErrNotFound)
				}
				if c.FlowStatusOf(cn.Flow) != domain.FlowOpen {
					return fmt.Errorf("change impact %s: flow %s is not open: %w", cn.ID, cn.Flow, ErrConflict)
				}
			}
			if cn.Via != "" && !inBatch[cn.Via] && !isKnown(cn.Via) {
				return fmt.Errorf("change impact %s: unknown via %s: %w", cn.ID, cn.Via, ErrInvalid)
			}
			if cn.Pre != nil {
				var n domain.Node
				if !b.Contains(*cn.Pre) {
					// a restoration starts from the tombstone that deleted the node, which no state holds (ADR 0172)
					if n, err = tx.Node(ctx, *cn.Pre); err != nil && !errors.Is(err, ErrNotFound) {
						return err
					} else if err != nil || !n.Deleted {
						return fmt.Errorf("change impact %s: pre %s is not in commit %s (the node moved on): %w", cn.ID, cn.Pre, b.ID, ErrConflict)
					}
				} else if n, err = tx.Node(ctx, *cn.Pre); err != nil {
					return err
				}
				pres := presOf(cn.Flow)
				if pres.has(cn.Pre.ID) {
					return fmt.Errorf("change impact %s: node %s is already in the change: %w", cn.ID, cn.Pre.ID, ErrConflict)
				}
				pres.add(cn.Pre.ID)
				if (cn.Key != "" && cn.Key != n.Key) || (cn.Type != "" && cn.Type != n.Type) {
					return fmt.Errorf("change impact %s: key / type do not match node %s: %w", cn.ID, n.Ref(), ErrInvalid)
				}
				cn.Key, cn.Type = n.Key, n.Type
				if got, want := domain.NamespaceOf(n.Namespace), domain.NamespaceOf(c.Namespace); got != want {
					return fmt.Errorf("change impact %s: %s belongs to namespace %q, the change acts on %q: %w", cn.ID, n.Key, got, want, ErrInvalid)
				}
			}
			if cn.Pre == nil && cn.Type != "" {
				if err := ix.checkNode(domain.NamespaceOf(c.Namespace), cn.Type); err != nil {
					return fmt.Errorf("change impact %s: %w", cn.ID, err)
				}
			}
			if propose && cn.Intent == domain.IntentCreated {
				if err := g.checkPlannedKey(ctx, tx, c, b, cn); err != nil {
					return err
				}
			}
			cn.CreatedAt = g.now()
			if err := cn.Validate(); err != nil {
				return fmt.Errorf("change impact %s: %v: %w", cn.ID, err, ErrInvalid)
			}
			cn := cn
			if propose {
				if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: cn.ID, Op: domain.ImpactProposed, Flow: cn.Flow, Execution: cn.Execution, State: &cn}); err != nil {
					return err
				}
			}
			out = append(out, cn)
		}
		if c.Status == domain.ChangeDraft {
			c.Status = domain.ChangeActive
			return tx.PutChange(ctx, c)
		}
		return nil
	}()
	return out, err
}

// presence tells the nodes a flow of a change holds an impact on (by their pre version): the impacts are looked through
// for the first nodes asked, indexed once more are asked (a declaration names one node, a proposal may name many).
type presence struct {
	impacts domain.Impacts
	fv      *flowNodes
	added   map[domain.NodeID]bool
	all     map[domain.NodeID]bool
	asks    int
}

func (p *presence) has(id domain.NodeID) bool {
	if p.added[id] {
		return true
	}
	if p.all == nil && p.asks < 4 {
		p.asks++
		return lastIndex(p.impacts.OfNode(id), func(cn domain.ChangeImpact) bool { return cn.Pre != nil && cn.Pre.ID == id && p.fv.visible(cn) }) >= 0
	}
	if p.all == nil {
		p.all = map[domain.NodeID]bool{}
		for _, cn := range p.impacts.All() {
			if cn.Pre != nil && p.fv.visible(cn) {
				p.all[cn.Pre.ID] = true
			}
		}
	}
	return p.all[id]
}

func (p *presence) add(id domain.NodeID) {
	if p.added == nil {
		p.added = map[domain.NodeID]bool{}
	}
	p.added[id] = true
}

// checkPlannedKey refuses a planned creation whose key a node already holds, or the change already acts on in the
// flow (createTx makes the same checks when the node is created).
func (g *Graph) checkPlannedKey(ctx context.Context, tx Tx, c domain.Change, b domain.Commit, cn domain.ChangeImpact) error {
	if nid, err := tx.NodeIDByKey(ctx, domain.NamespaceOf(c.Namespace), cn.Key); err == nil {
		if _, ok := b.Nodes[nid]; ok {
			return fmt.Errorf("cannot plan %s (%s): the reference commit of change %s already holds it: %w", cn.Key, cn.Type, c.ID, ErrConflict)
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	fv := g.newFlowNodes(tx, c, cn.Flow)
	for _, other := range c.Impacts().All() {
		if other.Key == cn.Key && other.ID != cn.ID && fv.visible(other) {
			return fmt.Errorf("cannot plan %s (%s): the change already holds it as change impact %s: %w", cn.Key, cn.Type, other.ID, ErrConflict)
		}
	}
	return nil
}

// ListChangeImpacts returns the change impacts of a change.
func (g *Graph) ListChangeImpacts(ctx context.Context, id domain.ChangeID) (out []domain.ChangeImpact, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		out = c.Impacts().Slice()
		return nil
	})
	return
}

// ImpactType returns the type of a change impact of a change, "" when the change holds no such impact: what a gate on
// the type of the node an operation touches reads, without copying the impacts of the change.
func (g *Graph) ImpactType(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID) (typ string, err error) {
	err = g.repo.InTx(platform.ReadOnly(ctx), func(tx Tx) error {
		if _, ok := memoHead(ctx, id); !ok {
			if _, err := tx.ChangeHead(ctx, id); err != nil {
				return err
			}
		}
		st, err := g.draftState(ctx, tx, id)
		if err != nil {
			return err
		}
		if cn, ok := st.impact(impact); ok {
			typ = cn.Type
		}
		return nil
	})
	return
}

// ImpactsOf returns the change impacts one execution (an Activity Run, architecture plan "Activity concept")
// declared: its Pre (the context graph) and Post (the modified graph) node versions, scoped to exactly what that
// run touched rather than the whole change. Empty when the execution declared none.
func (g *Graph) ImpactsOf(ctx context.Context, id domain.ChangeID, execution string) (out []domain.ChangeImpact, err error) {
	all, err := g.ListChangeImpacts(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, cn := range all {
		if cn.Execution == execution {
			out = append(out, cn)
		}
	}
	return out, nil
}

// ImpactNodeReview accepts or rejects a proposed change impact; accepting checks its draft, if any, as the version
// written from it will be (ADR 0079) and writes nothing: commit does. The comment is mandatory: it is kept in the review
// history and, once the change commits, on the version itself, so the origin of a version can be read from the node. The
// verdict is made by a review of one entry, submitted at once (ADR 0105).
func (g *Graph) ImpactNodeReview(ctx context.Context, id domain.ChangeID, node domain.ChangeImpactID, status domain.NodeReview, by, comment string) (domain.ChangeImpact, error) {
	return g.ImpactNodeReviewOn(ctx, id, "", "", node, status, by, comment)
}

// ImpactNodeReviewOn reviews a change impact as a flow sees it (ADR 0025): on a flow the review is a
// candidate until the flow is adopted, and execution is the action run that made it. As every verdict (ADR 0105), it
// is the submission of a review: one of a single entry, whose record the graph writes before the verdict.
//
//goap:operation impact.review reviewer
func (g *Graph) ImpactNodeReviewOn(ctx context.Context, id domain.ChangeID, flow, execution string, node domain.ChangeImpactID, status domain.NodeReview, by, comment string) (cn domain.ChangeImpact, err error) {
	if err := g.allow(ctx, id, operations.ImpactReview); err != nil {
		return cn, err
	}
	out, err := g.reviewBatch(ctx, id, domain.ReviewBatch{Flow: flow, Execution: execution, By: by,
		Verdicts: []domain.ImpactVerdict{{Impact: node, Status: status, Comment: comment}}})
	if err != nil {
		return cn, err
	}
	return out[0], nil
}

func checkVerdict(status domain.NodeReview, comment string) error {
	if status != domain.ReviewAccepted && status != domain.ReviewRejected {
		return fmt.Errorf("a change impact is reviewed as accepted or rejected, not %q: %w", status, ErrInvalid)
	}
	if strings.TrimSpace(comment) == "" {
		return fmt.Errorf("a review needs a comment: %w", ErrInvalid)
	}
	return nil
}

// errOriginsPending is the refusal of an acceptance whose origins are not accepted yet (checkOrigins): a batch of
// reviews retries it once the others stood.
var errOriginsPending = errors.New("settle the origins first")

// reviewScope is what the verdicts of a batch on the main flow share (they are staged, ADR 0144): the change, the
// change impacts the flow sees, the type index and the reader of the drafts, read once for the batch and kept up to date
// by its verdicts, so that a batch of N verdicts reads and copies the N impacts of the change once, not once per
// verdict (an import reviews every node of a commit in one batch).
type reviewScope struct {
	c  domain.Change
	at map[domain.ChangeImpactID]int // position in c.Nodes
	// seen are the change impacts the main flow sees (its own live ones, as they are), seenAt their positions
	seen   []domain.ChangeImpact
	seenAt map[domain.ChangeImpactID]int
	ix     *typeIndex
	r      *draftReader
}

// newReviewScope reads what the verdicts of a batch on the main flow share.
func (g *Graph) newReviewScope(ctx context.Context, tx Tx, id domain.ChangeID) (*reviewScope, error) {
	w, err := g.workOn(ctx, tx, id, domain.MainFlow)
	if err != nil {
		return nil, err
	}
	// verdict records the verdicts in them: copies of the change's (shared with its view, changeOpen)
	w.c.Nodes = w.c.Impacts().Slice()
	s := &reviewScope{c: w.c, seen: w.seen.Slice(), ix: w.ix, at: make(map[domain.ChangeImpactID]int, len(w.c.Nodes)), seenAt: make(map[domain.ChangeImpactID]int, w.seen.Len())}
	for i, cn := range s.c.Nodes {
		s.at[cn.ID] = i
	}
	for i, cn := range s.seen {
		s.seenAt[cn.ID] = i
	}
	// the drafts do not change by a review: one reader serves every verdict
	if s.r, err = g.newDraftReader(ctx, tx, s.c, "", domain.ImpactsOf(s.seen)); err != nil {
		return nil, err
	}
	return s, nil
}

// verdict records a verdict of the batch in the scope, as the fold of the change records it.
func (s *reviewScope) verdict(cn domain.ChangeImpact) {
	if i, ok := s.at[cn.ID]; ok {
		s.c.Nodes[i] = cn
	}
	if i, ok := s.seenAt[cn.ID]; ok {
		s.seen[i] = cn
	}
}

// reviewTx is one verdict inside a transaction: the policy, the acceptance gate, the reviewed event. Nothing is
// written when it refuses. reviewID is the key of the submitted review that makes the verdict, stamped on the review of
// the impact (ADR 0080), and causation the log entry of its record, the cause of the reviewed event (ADR 0105). A
// verdict of a batch on the main flow is staged and reads the change from the scope of the batch (scope and stage set).
func (g *Graph) reviewTx(ctx context.Context, tx Tx, stage *impactStage, scope *reviewScope, id domain.ChangeID, flow, execution string, node domain.ChangeImpactID, status domain.NodeReview, by, comment, reviewID, causation string) (cn domain.ChangeImpact, err error) {
	var c domain.Change
	var seen domain.ChangeImpact
	if scope != nil {
		c, flow = scope.c, ""
		i, ok := scope.at[node]
		if !ok {
			return cn, fmt.Errorf("change impact %s not in change %s: %w", node, id, ErrNotFound)
		}
		cn = c.Nodes[i]
		j, ok := scope.seenAt[node]
		if !ok {
			return cn, fmt.Errorf("change impact %s is not on the flow %q of change %s: %w", node, flow, id, ErrNotFound)
		}
		seen = scope.seen[j]
	} else {
		if c, err = changeOpen(ctx, tx, id); err != nil {
			return cn, err
		}
		flow = c.ResolveFlow(flow)
		if flow != "" {
			if _, ok := c.Flow(flow); !ok {
				return cn, fmt.Errorf("flow %s: %w", flow, ErrNotFound)
			}
			if c.FlowStatusOf(flow) != domain.FlowOpen {
				return cn, fmt.Errorf("flow %s is not open: %w", flow, ErrConflict)
			}
		}
		if cn, err = findChangeImpact(c, node); err != nil {
			return cn, err
		}
		if seen, err = g.newFlowNodes(tx, c, flow).find(ctx, node); err != nil {
			return cn, err
		}
	}
	if seen.Review != domain.ReviewProposed {
		return cn, fmt.Errorf("change impact %s is already %s: %w", node, seen.Review, ErrConflict)
	}
	if g.ReviewPolicy != nil {
		// the facts of the change are its items, folded with its log
		if err := g.ReviewPolicy.Review(domain.ReviewRequest{Impact: seen, Reviewer: by, Status: status, Items: c.Items}); err != nil {
			return cn, fmt.Errorf("review of %s refused: %v: %w", node, err, ErrInvalid)
		}
	}
	// an accepted review is gated by what a version must satisfy (ADR 0079): validators, required links, link
	// attributes, the origins gate, checked on the draft; a refusal leaves the review proposed. Nothing is
	// written: the version is written when the change commits
	if status == domain.ReviewAccepted {
		if err := g.checkSettled(ctx, tx, c, node); err != nil {
			return cn, err
		}
		if err := g.checkModifies(ctx, tx, c, flow, seen); err != nil {
			return cn, err
		}
	}
	if status == domain.ReviewAccepted && seen.Post != nil {
		if scope != nil {
			err = g.checkAcceptedIn(ctx, tx, scope, seen)
		} else {
			err = g.checkAccepted(ctx, tx, id, flow, node)
		}
		if err != nil {
			return cn, err
		}
	}
	r := domain.Review{Status: status, By: by, Comment: comment, At: g.now(), Flow: flow, Execution: execution, ReviewID: reviewID}
	if flow == "" {
		cn.Review = status
	}
	cn.Reviews = append(slices.Clip(cn.Reviews), r)
	e := domain.ImpactEvent{Change: id, Impact: cn.ID, Op: domain.ImpactReviewed, Flow: flow, Execution: execution, By: by, Review: &r, Causation: causation}
	if stage != nil {
		if err := g.stage(ctx, tx, stage, e); err != nil {
			return cn, err
		}
		if scope != nil {
			scope.verdict(cn)
		}
		return cn, nil
	}
	return cn, g.emit(ctx, tx, e)
}

// ImpactNodeReviewBatch submits a review (ADR 0080, 0105): every verdict is made as ImpactNodeReviewOn would make it,
// and either all of them stand or none does. A refusal (the review policy, the gate of an acceptance, an impact no
// longer proposed) names the verdict and writes nothing. The record of the submitted review is written first, then the
// verdicts, each reviewed event naming it as its cause; every review carries its key. The batch's Item, when set, is
// that record (the use case's review built up, now submitted: kind review, status submitted, its entries the verdicts);
// else the graph writes one, keyed by ID (allocated when empty). A verdict accepting a node whose origins are accepted
// by the same batch stands: the origins are settled first.
func (g *Graph) ImpactNodeReviewBatch(ctx context.Context, id domain.ChangeID, b domain.ReviewBatch) (out []domain.ChangeImpact, err error) {
	if err := g.allow(ctx, id, operations.ImpactReview); err != nil {
		return out, err
	}
	return g.reviewBatch(ctx, id, b)
}

// reviewBatch is ImpactNodeReviewBatch once the operation is allowed.
func (g *Graph) reviewBatch(ctx context.Context, id domain.ChangeID, b domain.ReviewBatch) (out []domain.ChangeImpact, err error) {
	if len(b.Verdicts) == 0 {
		return nil, fmt.Errorf("a review batch needs at least one verdict: %w", ErrInvalid)
	}
	label := func(n int, impact string) string {
		if len(b.Verdicts) == 1 {
			return ""
		}
		return fmt.Sprintf("verdict %d (%s): ", n+1, impact)
	}
	seenImpact := map[domain.ChangeImpactID]bool{}
	for n, v := range b.Verdicts {
		if err := checkVerdict(v.Status, v.Comment); err != nil {
			return nil, fmt.Errorf("%s%w", label(n, string(v.Impact)), err)
		}
		if seenImpact[v.Impact] {
			return nil, fmt.Errorf("verdict %d: change impact %s is reviewed twice in the batch: %w", n+1, v.Impact, ErrInvalid)
		}
		seenImpact[v.Impact] = true
	}
	it, err := g.reviewRecord(b)
	if err != nil {
		return nil, err
	}
	if err := g.authorizeItems(ctx, id, []domain.ChangeItem{it}); err != nil {
		return nil, err
	}
	key := domain.ReviewRecordOf(it.Data).Key
	err = g.changeTx(ctx, id, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		it := it
		it.Flow = c.ResolveFlow(b.Flow) // the verdicts are made on this flow (ADR 0032 §6)
		impacts := make([]domain.ChangeImpactID, len(b.Verdicts))
		for i, v := range b.Verdicts {
			impacts[i] = v.Impact
		}
		if err := g.prefetchBases(ctx, tx, id, it.Flow, impacts); err != nil {
			return err
		}
		if err := putItem(ctx, tx, id, it); err != nil {
			return err
		}
		// the verdicts judge each other (the origins of an acceptance) and are one entry of the log (ADR 0144): staged,
		// then flushed. On a flow they are not: what a flow sees is read from the log (flowNodes)
		var stage *impactStage
		var scope *reviewScope
		if it.Flow == "" {
			stage = &impactStage{}
			if scope, err = g.newReviewScope(ctx, tx, id); err != nil {
				return err
			}
		}
		out = make([]domain.ChangeImpact, len(b.Verdicts))
		pending := make([]int, len(b.Verdicts))
		for i := range pending {
			pending[i] = i
		}
		name := func(i int) string {
			v := b.Verdicts[i]
			if c, err := tx.Change(ctx, id); err == nil {
				if cn, err := findChangeImpact(c, v.Impact); err == nil {
					return label(i, cn.Key)
				}
			}
			return label(i, string(v.Impact))
		}
		for len(pending) > 0 {
			var next []int
			var lastErr error
			for _, i := range pending {
				v := b.Verdicts[i]
				cn, err := g.reviewTx(ctx, tx, stage, scope, id, b.Flow, b.Execution, v.Impact, v.Status, b.By, strings.TrimSpace(v.Comment), key, string(it.ID))
				switch {
				case err == nil:
					out[i] = cn
				case errors.Is(err, errOriginsPending):
					next, lastErr = append(next, i), fmt.Errorf("%s%w", name(i), err)
				default:
					return fmt.Errorf("%s%w", name(i), err)
				}
			}
			if len(next) == len(pending) {
				return lastErr // no progress: the origins are not settled by this batch
			}
			pending = next
		}
		if stage != nil {
			return g.flush(ctx, tx, stage)
		}
		return nil
	})
	if err != nil {
		out = nil
	}
	return out, err
}

// putReviewRecord writes the record of a review the graph submits on its own (a merge accepting what it commits, ADR
// 0105) on the main flow of a change: by the component, its entries decided. It returns the key of the review and the
// log entry of the record, the cause of the reviewed events that follow.
func (g *Graph) putReviewRecord(ctx context.Context, tx Tx, change domain.ChangeID, by, comment string, entries []domain.ReviewEntry) (key, cause string, err error) {
	rec := domain.ReviewRecord{Key: "REV-" + g.newID(), Comment: comment, Status: domain.ReviewStatusSubmitted, Entries: entries, By: by, SubmittedAt: g.now()}
	it := domain.ChangeItem{ID: domain.ItemID(g.newID()), Type: domain.TypeReview, Status: domain.ItemProposed, Data: rec.Data(), ProducedBy: by, CreatedAt: g.now()}
	if err := g.checkItem(it); err != nil {
		return "", "", fmt.Errorf("item %s: %v: %w", it.ID, err, ErrInvalid)
	}
	return rec.Key, string(it.ID), putItem(ctx, tx, change, it)
}

// reviewRecord is the item recording the submitted review of a batch: the batch's own, checked against its verdicts, or
// one the graph makes of them.
func (g *Graph) reviewRecord(b domain.ReviewBatch) (domain.ChangeItem, error) {
	var it domain.ChangeItem
	if b.Item != nil {
		it = *b.Item
		rec := domain.ReviewRecordOf(it.Data)
		if it.Type != domain.TypeReview || rec.Status != domain.ReviewStatusSubmitted {
			return it, fmt.Errorf("the item of a review batch is the submitted review, not a %s %s item: %w", rec.Status, it.Type, ErrInvalid)
		}
		if b.ID != "" && rec.Key != b.ID {
			return it, fmt.Errorf("review batch %s carries the record of review %s: %w", b.ID, rec.Key, ErrInvalid)
		}
		if len(rec.Entries) != len(b.Verdicts) {
			return it, fmt.Errorf("review %s has %d entries for %d verdicts: %w", rec.Key, len(rec.Entries), len(b.Verdicts), ErrInvalid)
		}
		for _, v := range b.Verdicts {
			i := slices.IndexFunc(rec.Entries, func(e domain.ReviewEntry) bool { return e.Impact == string(v.Impact) })
			if i < 0 || rec.Entries[i].Outcome != domain.ReviewOutcome(v.Status) {
				return it, fmt.Errorf("review %s does not record the verdict on %s: %w", rec.Key, v.Impact, ErrInvalid)
			}
		}
	} else {
		key := b.ID
		if key == "" {
			key = "REV-" + g.newID()
		}
		rec := domain.ReviewRecord{Key: key, Flow: b.Flow, Status: domain.ReviewStatusSubmitted, By: b.By, SubmittedAt: g.now()}
		for _, v := range b.Verdicts {
			rec.Entries = append(rec.Entries, domain.ReviewEntry{Impact: string(v.Impact), Comment: strings.TrimSpace(v.Comment), Outcome: domain.ReviewOutcome(v.Status)})
		}
		it = domain.ChangeItem{Type: domain.TypeReview, Data: rec.Data(), ProducedBy: b.By, Execution: b.Execution}
	}
	if it.ID == "" {
		it.ID = domain.ItemID(g.newID())
	}
	if it.Status == "" {
		it.Status = domain.ItemProposed
	}
	it.CreatedAt = g.now()
	if err := g.checkItem(it); err != nil {
		return it, fmt.Errorf("item %s: %v: %w", it.ID, err, ErrInvalid)
	}
	return it, nil
}

// ReopenImpacts sends decided change impacts of the main flow back to proposed (a review event, as when a change goes
// back to a state, ADR 0058): what was accepted must be reviewed again, what was rejected is reworked (its working
// version was kept, ADR 0076 §5b). A use case calls it when the ground of an acceptance falls (ADR 0075: a derogation
// expired); the graph reads no clock and names no reason. A proposed impact is left as it is; the comment is
// mandatory.
func (g *Graph) ReopenImpacts(ctx context.Context, id domain.ChangeID, impacts []domain.ChangeImpactID, comment string) (reopened []domain.ChangeImpactID, err error) {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return nil, fmt.Errorf("reopening a review needs a comment: %w", ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		reopened = nil
		c, err := changeOpen(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, imp := range impacts {
			cn, err := findChangeImpact(c, imp)
			if err != nil {
				return err
			}
			if cn.Review != domain.ReviewAccepted && cn.Review != domain.ReviewRejected {
				continue
			}
			r := domain.Review{Status: domain.ReviewProposed, By: g.caller(ctx), At: g.now(), Comment: comment}
			if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: cn.ID, Op: domain.ImpactReviewed, By: r.By, Review: &r}); err != nil {
				return err
			}
			reopened = append(reopened, cn.ID)
		}
		return nil
	})
	return
}

// checkModifies refuses to accept a change impact that modifies nothing (ADR 0102): a candidate never checked out, or a
// draft left as it was checked out. Only a modification is accepted, whoever accepts it.
func (g *Graph) checkModifies(ctx context.Context, tx Tx, c domain.Change, flow string, cn domain.ChangeImpact) error {
	d, err := g.seenDraft(ctx, tx, c, flow, cn.ID)
	if err != nil {
		return err
	}
	if d == nil {
		return invalidf("change impact %s (%s) modifies nothing: check the node out and change it, or withdraw the impact", cn.Key, cn.ID)
	}
	if ok, err := g.draftModifies(ctx, tx, d); err != nil {
		return err
	} else if !ok {
		return invalidf("change impact %s (%s) modifies nothing: its draft is the version it was checked out from", cn.Key, cn.ID)
	}
	return nil
}
