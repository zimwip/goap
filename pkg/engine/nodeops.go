package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/risk"
	"github.com/zimwip/goap/pkg/verify"
)

// impactWriteRetries bounds the retry of a single change-impact write on
// graph.ErrConflict (ADR 0031, gap 6): with concurrent processes now sharing
// one change's main branch by default, a transient version clash on a node
// the graph layer re-validates internally (e.g. a concurrent WriteNode) is
// expected rather than rare. graph.ErrConflict is a single sentinel shared by
// several non-transient conditions too (an unresolved merge, a flow that
// needs a decision, a key already in use); retrying those is harmless (the
// graph layer re-checks live state on every call, so a non-transient cause
// fails identically each time) but not effective — this is a best-effort net
// for the genuinely transient case, not a fix for every ErrConflict cause.
const impactWriteRetries = 3

func retryOnConflict[T any](call func() (T, error)) (T, error) {
	var (
		out T
		err error
	)
	for attempt := 0; attempt < impactWriteRetries; attempt++ {
		out, err = call()
		if err == nil || !errors.Is(err, graph.ErrConflict) {
			return out, err
		}
	}
	return out, err
}

// ChangeImpactsFromBlackboard builds the change impact snapshot given to scripts.
func ChangeImpactsFromBlackboard(bb domain.Blackboard) []dsl.ChangeImpact {
	view := func(r *domain.NodeRef) *dsl.Node {
		if r == nil {
			return nil
		}
		n := &dsl.Node{ID: string(r.ID), Version: int(r.Version)}
		if v, ok := bb.Nodes[*r]; ok {
			n.Key, n.Type, n.State, n.Props = v.Key, v.Type, v.State, v.Properties
		}
		return n
	}
	end := func(r domain.NodeRef) dsl.LinkEnd {
		e := dsl.LinkEnd{ID: string(r.ID), Version: int(r.Version)}
		if n, ok := bb.Neighbors[r]; ok {
			e.Key, e.Type = n.Key, n.Type
		} else if v, ok := bb.Nodes[r]; ok {
			e.Key, e.Type = v.Key, v.Type
		}
		return e
	}
	out := make([]dsl.ChangeImpact, 0, len(bb.Change.Nodes))
	for _, cn := range bb.Change.Nodes {
		x := dsl.ChangeImpact{ID: string(cn.ID), Key: cn.Key, Type: cn.Type, Intent: string(cn.Intent), Rationale: cn.Rationale, Review: string(cn.Review),
			Planned: cn.Post == nil, Pre: view(cn.Pre), Post: view(cn.Post), Landed: view(cn.Landed), Items: []string{}, Links: []dsl.Link{}}
		if cn.Post != nil {
			x.CheckedOut = bb.Nodes[*cn.Post].CheckedOut
		}
		if cn.Post != nil {
			for _, l := range bb.Nodes[*cn.Post].Out {
				x.Links = append(x.Links, dsl.Link{ID: string(l.ID), Type: l.Type, From: dsl.LinkEnd{ID: string(cn.Post.ID), Version: int(cn.Post.Version), Key: cn.Key, Type: cn.Type}, To: end(l.To)})
			}
		}
		for _, id := range cn.Items {
			x.Items = append(x.Items, string(id))
		}
		out = append(out, x)
	}
	return out
}

// applyNodeOps applies the change impact operations a script buffered, in order:
// a declaration adds a change impact, a write edits its working version (checked
// out on the first write, ADR 0076), a review accepts or rejects it (the working
// version stays one until the change lands, ADR 0077), a transition moves it along its lifecycle, a cancel drops the
// working version. References ("#nN")
// name the change impacts declared earlier by the same script; a key names a
// change impact the process sees. On a flow branch the process sees the change
// nodes of its flow, and what it declares and writes stays on the flow until it
// is adopted (ADR 0025).
func (e *Engine) applyNodeOps(ctx context.Context, p *Process, ops []dsl.NodeOp, producedBy, execution string) ([]domain.ChangeImpactID, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	if p.ChangeID == "" {
		return nil, fmt.Errorf("process %s has no change attached: call goap-scheduler/attach first (ADR 0031)", p.ID)
	}
	bb, err := e.Graph.BlackboardIn(ctx, p.ChangeID, p.Flow)
	if err != nil {
		return nil, err
	}
	// the pre version of a change impact is the released one: the reference baseline, whatever option is active
	nodes, _, err := e.Graph.BaselineGraph(ctx, bb.Change.BaselineID)
	if err != nil {
		return nil, err
	}
	baseline := map[string]domain.NodeRef{}
	for _, n := range nodes {
		baseline[n.Key] = n.Ref()
	}
	byKey := map[string]domain.ChangeImpactID{} // stored change impacts, then the ones this script declares
	posts := map[domain.ChangeImpactID]domain.NodeRef{}
	out := map[domain.ChangeImpactID]bool{} // the change impacts with a working version (ADR 0076)
	for _, cn := range bb.Change.Nodes {
		if len(cn.Items) > 0 {
			continue // derived from items: decided through them
		}
		byKey[cn.Key] = cn.ID
		if cn.Post != nil {
			posts[cn.ID] = *cn.Post
			out[cn.ID] = graph.IsWorking(bb.Change, p.Flow, bb.Nodes[*cn.Post].Node)
		}
	}
	local := map[string]domain.ChangeImpactID{}
	var declared []domain.ChangeImpactID
	// A node a script declares as created is not recorded by the graph yet: a new node has no proposal (ADR 0077), its
	// creation is the one event of its impact and first version. It is held here, named by its reference and its key,
	// and created by the first operation that needs it: a write creates it with the properties it carries, any other
	// operation (and the end of the script) creates it bare.
	type creation struct {
		ref, key, typ, rationale string
	}
	pending := map[string]*creation{}
	var pendingOrder []*creation
	var create func(c *creation, props map[string]any) (domain.ChangeImpact, error)
	resolve := func(s string) (domain.ChangeImpactID, error) {
		if c := pending[s]; c != nil {
			cn, err := create(c, nil)
			return cn.ID, err
		}
		if strings.HasPrefix(s, "#") {
			if id, ok := local[s]; ok {
				return id, nil
			}
			return "", fmt.Errorf("unknown change impact reference %q", s)
		}
		if id, ok := byKey[s]; ok {
			return id, nil
		}
		return "", fmt.Errorf("no change impact for %q: declare it with impactNode or impactNodeCreate", s)
	}
	target := func(s string) (domain.NodeRef, error) { // a link target: a node written by the change, else the reference baseline
		if strings.HasPrefix(s, "#") {
			id, err := resolve(s)
			if err != nil {
				return domain.NodeRef{}, err
			}
			if ref, ok := posts[id]; ok {
				return ref, nil
			}
			return domain.NodeRef{}, fmt.Errorf("%s is not written yet: write it before linking to it", s)
		}
		if pending[s] != nil { // a creation declared earlier: a link needs the node
			if _, err := resolve(s); err != nil {
				return domain.NodeRef{}, err
			}
		}
		if id, ok := byKey[s]; ok {
			if ref, ok := posts[id]; ok {
				return ref, nil
			}
		}
		if ref, ok := baseline[s]; ok {
			return ref, nil
		}
		return domain.NodeRef{}, fmt.Errorf("unknown node %q", s)
	}
	// a bare type or link type written by an action (LLM output, script, human input) is one of the target
	// namespace of the change (ADR 0012): "Requirement" in a change of alm is alm@Requirement
	qualify := func(t string) string {
		if t == "" || strings.Contains(t, domain.TypeSep) || bb.Change.Namespace == "" {
			return t
		}
		return bb.Change.Namespace + domain.TypeSep + t
	}
	// the verification of what the action produces (ADR 0075): its produced entries are written as the effects appear,
	// before any review of the same script, so the review policy sees them
	vf := verifyOf(ctx)
	var model string
	if vf != nil {
		model = vf.model
	}
	verified := map[domain.ChangeImpactID]bool{} // the effects with a verification entry, written before or by this call
	for _, sub := range verify.Subjects(bb.Change.Items) {
		verified[domain.ChangeImpactID(sub.Impact)] = true
	}
	record := func(state string, impact domain.ChangeImpactID, extra map[string]any) error {
		data := map[string]any{verify.KeyState: state, verify.KeyAction: producedBy, verify.KeyImpact: string(impact)}
		for k, v := range extra {
			data[k] = v
		}
		return e.recordVerification(ctx, p, execution, producedBy, data)
	}
	produce := func(id domain.ChangeImpactID) error {
		if vf == nil || verified[id] {
			return nil
		}
		verified[id] = true
		d := map[string]any{verify.KeyState: verify.Produced, verify.KeyAction: vf.action, verify.KeyImpacts: []string{string(id)},
			verify.KeyOracle: vf.verify.Oracle, verify.KeyIndependent: vf.verify.IsIndependent()}
		if model != "" {
			d[verify.KeyModel] = model
		}
		return e.recordVerification(ctx, p, execution, producedBy, d)
	}
	create = func(c *creation, props map[string]any) (domain.ChangeImpact, error) {
		delete(pending, c.ref)
		delete(pending, c.key)
		cn, err := retryOnConflict(func() (domain.ChangeImpact, error) {
			return e.Graph.ImpactNodeCreate(ctx, p.ChangeID, graph.NodeCreate{Key: c.key, Type: c.typ, Properties: props, Rationale: c.rationale,
				Flow: p.Flow, Execution: execution, ProducedBy: producedBy})
		})
		if err != nil {
			return cn, err
		}
		local[c.ref], byKey[cn.Key] = cn.ID, cn.ID
		declared = append(declared, cn.ID)
		if cn.Post != nil {
			posts[cn.ID], out[cn.ID] = *cn.Post, true
		}
		return cn, produce(cn.ID)
	}
	for i, op := range ops {
		fail := func(err error) error { return fmt.Errorf("change impact operation %d (%s): %w", i, op.Op, err) }
		switch op.Op {
		case "declare":
			if domain.NodeIntent(op.Intent) == domain.IntentCreated {
				c := &creation{ref: op.Ref, key: op.Key, typ: qualify(op.Type), rationale: op.Rationale}
				pending[c.key] = c
				if c.ref != "" {
					pending[c.ref] = c
				}
				pendingOrder = append(pendingOrder, c)
				continue
			}
			cn := domain.ChangeImpact{Intent: domain.NodeIntent(op.Intent), Key: op.Key, Type: qualify(op.Type), Rationale: op.Rationale, ProducedBy: producedBy, Execution: execution, Flow: p.Flow}
			if cn.Intent == domain.IntentModified {
				ref, ok := baseline[op.Key]
				if !ok {
					return declared, fail(fmt.Errorf("unknown node %q in the reference baseline", op.Key))
				}
				cn.Pre = &ref
			}
			added, err := retryOnConflict(func() ([]domain.ChangeImpact, error) {
				return e.Graph.ProposeImpact(ctx, p.ChangeID, []domain.ChangeImpact{cn})
			})
			if err != nil {
				return declared, fail(err)
			}
			local[op.Ref], byKey[added[0].Key] = added[0].ID, added[0].ID
			declared = append(declared, added[0].ID)
			if err := produce(added[0].ID); err != nil {
				return declared, fail(err)
			}
		case "merge", "split":
			names := op.Sources
			if op.Op == "split" {
				names = []string{op.Node}
			}
			var srcs []graph.NodeName
			for _, s := range names {
				if id, err := resolve(s); err == nil {
					srcs = append(srcs, graph.NodeName{Impact: id})
				} else if strings.HasPrefix(s, "#") {
					return declared, fail(err)
				} else {
					srcs = append(srcs, graph.NodeName{Key: s})
				}
			}
			var into []graph.NodeCreate
			for _, x := range op.Into {
				into = append(into, graph.NodeCreate{Key: x.Key, Type: qualify(x.Type), Properties: x.Props, Rationale: x.Rationale, Flow: p.Flow, Execution: execution, ProducedBy: producedBy})
			}
			var res graph.Restructured
			var err error
			if op.Op == "merge" && len(into) == 1 {
				res, err = e.Graph.ImpactNodeMerge(ctx, p.ChangeID, graph.MergeInput{Sources: srcs, Into: into[0], Rationale: op.Rationale, Flow: p.Flow, Execution: execution})
			} else if op.Op == "split" && len(srcs) == 1 {
				res, err = e.Graph.ImpactNodeSplit(ctx, p.ChangeID, graph.SplitInput{Source: srcs[0], Into: into, Rationale: op.Rationale, Flow: p.Flow, Execution: execution})
			} else {
				err = fmt.Errorf("malformed %s", op.Op)
			}
			if err != nil {
				return declared, fail(err)
			}
			for i, cn := range res.Successors {
				if i < len(op.Into) {
					local[op.Into[i].Ref] = cn.ID
				}
			}
			for _, cn := range slices.Concat(res.Successors, res.Sources, res.Parents) {
				byKey[cn.Key] = cn.ID
				declared = append(declared, cn.ID)
				if cn.Post != nil {
					posts[cn.ID] = *cn.Post
				}
			}
			for _, cn := range slices.Concat(res.Successors, res.Parents) {
				out[cn.ID] = true
			}
			for _, cn := range res.Successors {
				if err := produce(cn.ID); err != nil {
					return declared, fail(err)
				}
			}
		case "write":
			var id domain.ChangeImpactID
			var err error
			propsIn := false // the creation carried the properties
			if c := pending[op.Node]; c != nil {
				var cn domain.ChangeImpact
				if cn, err = create(c, op.Props); err == nil {
					id, propsIn = cn.ID, true
				}
			} else {
				id, err = resolve(op.Node)
			}
			if err != nil {
				return declared, fail(err)
			}
			if op.State != "" {
				return declared, fail(fmt.Errorf("a lifecycle state is a transition of its own: use impactNodeTransition (ADR 0076)"))
			}
			// the first write checks the node out: its working version, edited in place by the next ones (ADR 0076)
			if !out[id] {
				cn, err := retryOnConflict(func() (domain.ChangeImpact, error) {
					return e.Graph.ImpactNodeCheckout(ctx, p.ChangeID, graph.NodeCheckout{Impact: id, Flow: p.Flow, Execution: execution})
				})
				if err != nil {
					return declared, fail(err)
				}
				posts[id], out[id] = *cn.Post, true
			}
			if len(op.Props) > 0 && !propsIn {
				if _, err := e.Graph.ImpactNodeUpdate(ctx, p.ChangeID, id, graph.NodeUpdate{Properties: op.Props, Flow: p.Flow, Execution: execution}); err != nil {
					return declared, fail(err)
				}
			}
			for _, l := range op.Links {
				to, err := target(l.To)
				if err != nil {
					return declared, fail(err)
				}
				if _, err := e.Graph.ImpactLinkCreate(ctx, p.ChangeID, id, graph.LinkWrite{Type: qualify(l.Type), To: to}, p.Flow, execution); err != nil {
					return declared, fail(err)
				}
			}
			for _, l := range op.RemoveLinks {
				if err := e.Graph.ImpactLinkDelete(ctx, p.ChangeID, domain.LinkID(l), p.Flow, execution); err != nil {
					return declared, fail(err)
				}
			}
			if err := produce(id); err != nil {
				return declared, fail(err)
			}
		case "transition":
			id, err := resolve(op.Node)
			if err != nil {
				return declared, fail(err)
			}
			cn, err := retryOnConflict(func() (domain.ChangeImpact, error) {
				return e.Graph.ImpactNodeTransition(ctx, p.ChangeID, graph.NodeTransition{NodeCheckout: graph.NodeCheckout{Impact: id, Flow: p.Flow, Execution: execution}, To: op.State})
			})
			if err != nil {
				return declared, fail(err)
			}
			posts[id] = *cn.Post
		case "cancel":
			id, err := resolve(op.Node)
			if err != nil {
				return declared, fail(err)
			}
			cn, err := e.Graph.ImpactNodeCancel(ctx, p.ChangeID, id, p.Flow, execution)
			if err != nil {
				return declared, fail(err)
			}
			out[id] = false
			if cn.Post != nil {
				posts[id] = *cn.Post
			} else {
				delete(posts, id)
			}
		case "remove":
			id, err := resolve(op.Node)
			if err != nil {
				return declared, fail(err)
			}
			if err := e.Graph.WithdrawImpact(ctx, p.ChangeID, id, p.Flow, execution); err != nil {
				return declared, fail(err)
			}
			delete(out, id)
			delete(posts, id)
			for k, v := range byKey {
				if v == id {
					delete(byKey, k)
				}
			}
		case "review":
			id, err := resolve(op.Node)
			if err != nil {
				return declared, fail(err)
			}
			status := domain.ReviewRejected
			if op.Accept {
				status = domain.ReviewAccepted
			}
			// an acceptance with a reserve stands on a derogation in force that covers the effect (ADR 0075 §2): without
			// one it is refused, before anything is written
			var derogation string
			if op.Reserve != "" {
				if !op.Accept {
					return declared, fail(fmt.Errorf("only an acceptance may carry a reserve"))
				}
				cur, err := e.Graph.BlackboardIn(ctx, p.ChangeID, p.Flow)
				if err != nil {
					return declared, fail(err)
				}
				d, ok := risk.Covering(cur.Change, e.clock(), string(id), op.Node, producedBy)
				if !ok || d.Key != op.Reserve {
					return declared, fail(fmt.Errorf("no derogation %s in force covers %s: it is open, unexpired and targets the effect or its action", op.Reserve, op.Node))
				}
				derogation = d.Key
			}
			if _, err := retryOnConflict(func() (domain.ChangeImpact, error) {
				return e.Graph.ImpactNodeReviewOn(ctx, p.ChangeID, p.Flow, execution, id, status, producedBy, op.Comment)
			}); err != nil {
				return declared, fail(err)
			}
			if verified[id] {
				final, extra := verify.Accepted, map[string]any{verify.KeyBy: producedBy}
				if status == domain.ReviewRejected {
					final = verify.Rejected
				}
				if derogation != "" {
					final, extra[verify.KeyDerogation] = verify.AcceptedWithReserve, derogation
				}
				if err := record(verify.Verified, id, map[string]any{verify.KeyBy: producedBy}); err != nil {
					return declared, fail(err)
				}
				if err := record(final, id, extra); err != nil {
					return declared, fail(err)
				}
			}
		default:
			return declared, fail(fmt.Errorf("unknown operation"))
		}
	}
	for _, c := range pendingOrder {
		if pending[c.key] == c {
			if _, err := create(c, nil); err != nil {
				return declared, fmt.Errorf("change impact of %s: %w", c.key, err)
			}
		}
	}
	return declared, nil
}

// verifying is what the engine knows of the verification of the action it runs (ADR 0075).
type verifying struct {
	action string
	model  string
	verify *methodology.Verify
}

type verifyKey struct{}

// withVerify marks the context of an action that declares a verification.
func withVerify(ctx context.Context, a methodology.Action) context.Context {
	if a.Verify == nil {
		return ctx
	}
	return context.WithValue(ctx, verifyKey{}, &verifying{action: a.Name, model: modelAlias(a), verify: a.Verify})
}

func verifyOf(ctx context.Context) *verifying {
	v, _ := ctx.Value(verifyKey{}).(*verifying)
	return v
}

// modelAlias is the model alias of an llm action, the producer a model oracle must differ from (ADR 0021).
func modelAlias(a methodology.Action) string {
	if a.Kind == methodology.KindLLM {
		return a.Model
	}
	return ""
}

// recordVerification writes a verification entry of the change log (fact.verification, ADR 0030) for an action run.
func (e *Engine) recordVerification(ctx context.Context, p *Process, execution, by string, data map[string]any) error {
	it := domain.ChangeItem{ID: domain.ItemID(uuid.NewString()), Kind: verify.KindVerification, Type: data[verify.KeyState].(string),
		Data: data, ProducedBy: by, Execution: execution, Flow: p.Flow}
	if err := it.Validate(); err != nil {
		return err
	}
	_, err := e.Graph.AddItems(ctx, p.ChangeID, []domain.ChangeItem{it})
	return err
}
