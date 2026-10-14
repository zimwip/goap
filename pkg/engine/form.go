package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/form"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

// formItems checks the values of the form of a human action and turns them into what the submission adds (ADR 0115):
// the items and change impact operations its code returns, else an artifact of the values and the links its link
// fields ask for.
func (e *Engine) formItems(ctx context.Context, p *Process, m *methodology.Compiled, action methodology.Action, step *Step, values map[string]any) ([]ItemInput, []dsl.NodeOp, error) {
	if p.ChangeID == "" {
		return nil, nil, fmt.Errorf("process %s has no change attached: %w", p.ID, ErrInvalidState)
	}
	bb, err := e.Graph.BlackboardIn(ctx, p.ChangeID, p.Flow)
	if err != nil {
		return nil, nil, err
	}
	nodes, _, err := e.Graph.BaselineGraph(ctx, bb.Change.BaselineID)
	if err != nil {
		return nil, nil, err
	}
	env, impacted := formEnv(bb, nodes)
	clean, issues := form.Validate(*action.Form, values, env)
	if len(issues) > 0 {
		return nil, nil, errors.Join(graph.ErrInvalid, fmt.Errorf("the form of %s: %w", action.Name, issues))
	}
	if action.Code != "" {
		return e.runFormCode(ctx, p, m, action, step, bb, clean)
	}
	typ := action.Form.Artifact
	if typ == "" {
		typ = action.Name
	}
	items := []ItemInput{{Kind: "artifact", Type: typ, Data: clean}}
	return items, linkOps(form.Links(*action.Form, clean), impacted), nil
}

// runFormCode runs the code of a human action on the values of its form, as a script action runs.
func (e *Engine) runFormCode(ctx context.Context, p *Process, m *methodology.Compiled, action methodology.Action, step *Step, bb domain.Blackboard, values map[string]any) ([]ItemInput, []dsl.NodeOp, error) {
	exec, ok := e.Executors[methodology.KindScript]
	if !ok {
		return nil, nil, fmt.Errorf("no script executor to run the code of %s", action.Name)
	}
	var agentMCPs []string
	if ag, ok := m.Agent(p.Agent); ok {
		agentMCPs = ag.MCPs
	}
	host := e.newHost(p, action, agentMCPs)
	host.step, host.base = p.Pending.Step, len(step.LLMCalls)
	res, err := exec.Execute(ctx, ActionContext{Process: p, Action: action, Blackboard: bb, Graph: e.Graph, Host: host, Step: p.Pending.Context, Form: values})
	host.record(step)
	for _, l := range res.Logs {
		l.Step = p.Pending.Step
		step.Logs = append(step.Logs, l)
	}
	if err != nil {
		return nil, nil, errors.Join(graph.ErrInvalid, fmt.Errorf("the code of %s: %w", action.Name, err))
	}
	if res.Suspended {
		return nil, nil, fmt.Errorf("the code of the human action %s cannot wait for a sub-agent: %w", action.Name, graph.ErrInvalid)
	}
	if res.Output != "" {
		step.Output = res.Output
	}
	return res.Items, res.Nodes, nil
}

// formEnv resolves the node keys of a form: the nodes of the reference baseline, then the nodes the change writes or
// creates (in the change); impacted holds the keys of the nodes the change has an impact on.
func formEnv(bb domain.Blackboard, baseline []domain.Node) (form.Env, map[string]bool) {
	known := map[string]form.Node{}
	for _, n := range baseline {
		if !n.Deleted {
			known[n.Key] = form.Node{Key: n.Key, Type: n.Type, State: n.State, Namespace: n.Namespace}
		}
	}
	impacted := map[string]bool{}
	for _, cn := range bb.Change.Nodes {
		if cn.Superseded || cn.Review == domain.ReviewRejected {
			continue
		}
		impacted[cn.Key] = true
		n := known[cn.Key]
		n.Key, n.InChange = cn.Key, true
		if n.Type == "" {
			n.Type = cn.Type
		}
		if cn.Post != nil {
			if v, ok := bb.Nodes[*cn.Post]; ok {
				n.Type, n.State = v.Node.Type, v.Node.State
				if v.Node.Namespace != "" {
					n.Namespace = v.Node.Namespace
				}
			}
		}
		if n.Namespace == "" {
			n.Namespace = bb.Change.Namespace
		}
		known[cn.Key] = n
	}
	env := form.Env{
		Node: func(k string) (form.Node, bool) { n, ok := known[k]; return n, ok },
		IsA: func(typ, super string) bool {
			return typ == super || slices.Contains(bb.Supertypes[typ], super)
		},
	}
	return env, impacted
}

// linkOps are the change impact operations adding the links of a form: a source the change does not hold yet is
// declared modified, then each source is written once with its links.
func linkOps(links []form.Link, impacted map[string]bool) []dsl.NodeOp {
	var ops []dsl.NodeOp
	var order []string
	bySource := map[string][]dsl.NodeOpLink{}
	for _, l := range links {
		if _, ok := bySource[l.From]; !ok {
			order = append(order, l.From)
		}
		bySource[l.From] = append(bySource[l.From], dsl.NodeOpLink{Type: l.Type, To: l.To})
	}
	for _, from := range order {
		if !impacted[from] {
			ops = append(ops, dsl.NodeOp{Op: "declare", Intent: string(domain.IntentModified), Key: from, Rationale: "links asked by a form"})
		}
		ops = append(ops, dsl.NodeOp{Op: "write", Node: from, Links: bySource[from]})
	}
	return ops
}
