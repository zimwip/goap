package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// Options of a change (ADR 0009 §3, ADR 0032 §6): an option is a flow branch opened as a hypothesis, forked from the
// main flow (Intent derive/revise) or from another open option (Intent refine: a sub-branch isolating narrower work
// within it). Several are explored at once; one of them may be active: the change works on it (every call that names
// no flow goes to it, "main" names the main flow). Selecting an option adopts its flow (its versions join the change
// branch) and rejects the other open ones; rejecting one discards its flow.

// OpenOptionRequest opens an option of a change.
type OpenOptionRequest struct {
	Name       string
	Hypothesis string
	// Parent is the option this one is a sub-branch of ("" = forked from the main flow). Non-empty only makes
	// sense with Intent refine: isolating narrower work within an already-open option.
	Parent string
	// Intent says why the option exists relative to Parent (derive, revise or refine; "" = unspecified).
	Intent domain.OptionIntent
	// Activate makes the new option the one the change works on.
	Activate bool
	By       string
}

// OpenOption opens an option: a flow branch of its own, forked from the main flow (or, with Parent set, from
// another open option - refine), that invalidates nothing.
func (g *Graph) OpenOption(ctx context.Context, id domain.ChangeID, in OpenOptionRequest) (f domain.Flow, err error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return f, fmt.Errorf("an option needs a name: %w", ErrInvalid)
	}
	if !domain.ValidOptionIntent(in.Intent) {
		return f, fmt.Errorf("unknown option intent %q: %w", in.Intent, ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := flowChange(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, o := range c.Options() {
			if o.Status == domain.FlowOpen && strings.EqualFold(o.Option.Name, name) {
				return fmt.Errorf("change %s already has an open option %q: %w", id, name, ErrConflict)
			}
		}
		if in.Parent != "" {
			if _, err := openOption(c, in.Parent); err != nil {
				return fmt.Errorf("parent option: %w", err)
			}
		}
		flow := g.newID()
		if err := g.flowEvent(ctx, tx, id, domain.FlowEvent{Op: domain.FlowOpenOp, Flow: flow, Parent: in.Parent, Reason: in.Hypothesis, By: in.By,
			Option: &domain.OptionSpec{Name: name, Hypothesis: in.Hypothesis, Intent: in.Intent}}); err != nil {
			return err
		}
		if in.Activate {
			if err := g.flowEvent(ctx, tx, id, domain.FlowEvent{Op: domain.FlowActivateOp, Flow: flow, By: in.By}); err != nil {
				return err
			}
		}
		if c.Status == domain.ChangeDraft {
			c.Status = domain.ChangeActive
			if err := tx.PutChange(ctx, c); err != nil {
				return err
			}
		}
		f, err = optionOf(ctx, tx, id, flow)
		return err
	})
	return
}

// ActivateOption makes an open option the one the change works on; "" or "main" goes back to the main flow.
func (g *Graph) ActivateOption(ctx context.Context, id domain.ChangeID, option, by string) (active string, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := flowChange(ctx, tx, id)
		if err != nil {
			return err
		}
		cur := c.ActiveOption()
		if option == "" || option == domain.MainFlow {
			if cur != "" {
				return g.flowEvent(ctx, tx, id, domain.FlowEvent{Op: domain.FlowDeactivateOp, Flow: cur, By: by})
			}
			return nil
		}
		if _, err := openOption(c, option); err != nil {
			return err
		}
		active = option
		if cur == option {
			return nil
		}
		return g.flowEvent(ctx, tx, id, domain.FlowEvent{Op: domain.FlowActivateOp, Flow: option, By: by})
	})
	return
}

// EvaluateOption records the evaluation of an open option: it moves to evaluated.
func (g *Graph) EvaluateOption(ctx context.Context, id domain.ChangeID, option, by, comment string) (f domain.Flow, err error) {
	if strings.TrimSpace(comment) == "" {
		return f, fmt.Errorf("an evaluation needs a comment: %w", ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := flowChange(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := openOption(c, option); err != nil {
			return err
		}
		if err := g.flowEvent(ctx, tx, id, domain.FlowEvent{Op: domain.FlowEvaluateOp, Flow: option, By: by, Comment: comment}); err != nil {
			return err
		}
		f, err = optionOf(ctx, tx, id, option)
		return err
	})
	return
}

// SelectOption selects an open option: its flow is adopted (what it wrote joins the change branch, ADR 0032 §2) and
// the other open options are rejected.
func (g *Graph) SelectOption(ctx context.Context, id domain.ChangeID, option, by string) (f domain.Flow, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := flowChange(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := g.selectOptionTx(ctx, tx, c, option, by); err != nil {
			return err
		}
		// a selection by hand settles the decision points that were choosing among the options (ADR 0009 §4)
		c, err = tx.Change(ctx, id)
		if err != nil {
			return err
		}
		for _, d := range c.DecisionPointsAt(g.now()) {
			if d.Pending() && slices.Contains(d.Options, option) {
				if err := g.decisionEvent(ctx, tx, id, domain.DecisionEvent{Op: domain.DecisionRuleOp, Point: d.ID, Outcome: domain.OutcomeDecided,
					Option: option, Confidence: 1, Justification: "option selected by hand", Human: true, By: by}); err != nil {
					return err
				}
			}
		}
		f, err = optionOf(ctx, tx, id, option)
		return err
	})
	return
}

// selectOptionTx rejects the other open options of c, then adopts the flow of option.
func (g *Graph) selectOptionTx(ctx context.Context, tx Tx, c domain.Change, option, by string) error {
	if _, err := openOption(c, option); err != nil {
		return err
	}
	for _, o := range c.Options() {
		if o.ID == option || o.Status != domain.FlowOpen {
			continue
		}
		if err := g.decideFlowTx(ctx, tx, c, o.ID, domain.FlowDiscardOp, by); err != nil {
			return err
		}
		var err error
		if c, err = tx.Change(ctx, c.ID); err != nil {
			return err
		}
	}
	return g.decideFlowTx(ctx, tx, c, option, domain.FlowAdoptOp, by)
}

// RejectOption rejects an open option: its flow is discarded, what it wrote stays on its branch for the audit.
func (g *Graph) RejectOption(ctx context.Context, id domain.ChangeID, option, by string) (f domain.Flow, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := flowChange(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := openOption(c, option); err != nil {
			return err
		}
		if err := g.decideFlowTx(ctx, tx, c, option, domain.FlowDiscardOp, by); err != nil {
			return err
		}
		f, err = optionOf(ctx, tx, id, option)
		return err
	})
	return
}

// Options lists the options of a change, oldest first.
func (g *Graph) Options(ctx context.Context, id domain.ChangeID) (out []domain.Flow, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		out = c.Options()
		return nil
	})
	return
}

func openOption(c domain.Change, option string) (domain.Flow, error) {
	f, ok := c.Flow(option)
	if !ok || f.Option == nil {
		return f, fmt.Errorf("option %s of change %s: %w", option, c.ID, ErrNotFound)
	}
	if f.Status != domain.FlowOpen {
		return f, fmt.Errorf("option %s is %s: %w", f.Option.Name, f.OptionStatus(), ErrConflict)
	}
	return f, nil
}

func optionOf(ctx context.Context, tx Tx, id domain.ChangeID, option string) (domain.Flow, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return domain.Flow{}, err
	}
	f, _ := c.Flow(option)
	return f, nil
}

// OptionNode is a node written by at least one option, with its version in the main flow and in each option (nil:
// not in the graph of that side, absent or retired).
type OptionNode struct {
	Node    domain.NodeID              `json:"node"`
	Key     string                     `json:"key"`
	Type    string                     `json:"type"`
	Main    *domain.NodeRef            `json:"main,omitempty"`
	Options map[string]*domain.NodeRef `json:"options"`
	// Props are the properties of the node on each side: "main" or the option id.
	Props map[string]map[string]any `json:"props,omitempty"`
}

// OptionComparison compares the open options of a change at a level (written or accepted): the nodes any of them
// changed from the main flow, both sides forking from the same graph (ADR 0032 §6).
type OptionComparison struct {
	Level   string        `json:"level"`
	Options []domain.Flow `json:"options"`
	Nodes   []OptionNode  `json:"nodes"`
}

// CompareOptions compares the open options of a change (every option when all is set) at a level.
func (g *Graph) CompareOptions(ctx context.Context, id domain.ChangeID, level string, all bool) (cmp OptionComparison, err error) {
	if level == "" {
		level = ViewWritten
	}
	if level != ViewWritten && level != ViewAccepted {
		return cmp, fmt.Errorf("options are compared at %s or %s, not %q: %w", ViewWritten, ViewAccepted, level, ErrInvalid)
	}
	cmp.Level = level
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		main, err := changeViewTx(ctx, g, tx, c, "", level)
		if err != nil {
			return err
		}
		views := map[string]domain.Baseline{}
		for _, o := range c.Options() {
			if !all && o.Status != domain.FlowOpen {
				continue
			}
			v, err := changeViewTx(ctx, g, tx, c, o.ID, level)
			if err != nil {
				return err
			}
			cmp.Options, views[o.ID] = append(cmp.Options, o), v
		}
		changed := map[domain.NodeID]bool{}
		for _, v := range views {
			for id, ver := range v.Nodes {
				if main.Nodes[id] != ver {
					changed[id] = true
				}
			}
			for id := range main.Nodes {
				if _, ok := v.Nodes[id]; !ok {
					changed[id] = true
				}
			}
		}
		ref := func(nodes map[domain.NodeID]domain.Version, id domain.NodeID) *domain.NodeRef {
			if v, ok := nodes[id]; ok {
				return &domain.NodeRef{ID: id, Version: v}
			}
			return nil
		}
		for id := range changed {
			on := OptionNode{Node: id, Main: ref(main.Nodes, id), Options: map[string]*domain.NodeRef{}, Props: map[string]map[string]any{}}
			sides := map[string]*domain.NodeRef{domain.MainFlow: on.Main}
			for oid, v := range views {
				on.Options[oid] = ref(v.Nodes, id)
				sides[oid] = on.Options[oid]
			}
			for side, r := range sides {
				if r == nil {
					continue
				}
				n, err := tx.Node(ctx, *r)
				if err != nil {
					return err
				}
				on.Key, on.Type, on.Props[side] = n.Key, n.Type, n.Properties
			}
			if on.Key == "" { // retired on every side it was on: name it from its history
				if vs, err := tx.Versions(ctx, id); err == nil && len(vs) > 0 {
					on.Key, on.Type = vs[0].Key, vs[0].Type
				}
			}
			cmp.Nodes = append(cmp.Nodes, on)
		}
		slices.SortFunc(cmp.Nodes, func(a, b OptionNode) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return
}

// ChangeGraph is the graph a call on a change reads (the nodes and the links between them): on an option (the flow
// named, else the active option), what the option wrote over the head of the change branch (ChangeView at written);
// otherwise the reference baseline of the change.
func (g *Graph) ChangeGraph(ctx context.Context, id domain.ChangeID, flow string) (nodes []domain.Node, links []domain.Link, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		flow := c.ResolveFlow(flow)
		f, ok := c.Flow(flow)
		if !ok || f.Option == nil {
			nodes, links, err = baselineGraphTx(ctx, tx, c.BaselineID)
			return err
		}
		v, err := changeViewTx(ctx, g, tx, c, flow, ViewWritten)
		if err != nil {
			return err
		}
		base, err := tx.NodesIn(ctx, v.ParentID, "")
		if err != nil {
			return err
		}
		for _, n := range base {
			if v.Contains(n.Ref()) {
				nodes = append(nodes, n)
			}
		}
		inBase := map[domain.NodeRef]bool{}
		for _, n := range nodes {
			inBase[n.Ref()] = true
		}
		for nid, ver := range v.Nodes {
			if r := (domain.NodeRef{ID: nid, Version: ver}); !inBase[r] {
				n, err := tx.Node(ctx, r)
				if err != nil {
					return err
				}
				nodes = append(nodes, n)
			}
		}
		slices.SortFunc(nodes, func(a, b domain.Node) int { return strings.Compare(a.Key, b.Key) })
		links, err = linksWithin(ctx, tx, v, nodes)
		return err
	})
	return
}
