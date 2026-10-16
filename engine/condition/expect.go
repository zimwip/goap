package condition

import (
	"fmt"
	"strconv"
	"strings"
)

// Expectation is the expected outcome of an action, expressed as a pattern
// on the domain axis. It compiles to a CEL condition so that the expected
// link is at the same time the action specification, its success criterion
// and a plannable effect.
//
// For each change impact x of the change (ADR 0024) that modifies a node (filtered by
// Where), the change must contain a change impact whose node was written, either:
//   - Produce.Op == "update_node": a new version of x's node;
//   - Produce.Op == "create_node": a created node of type Produce.NodeType linked
//     to x's node by Link.Type (direction "out": new -> x, "in": x -> new).
type Expectation struct {
	ForEach string      `yaml:"forEach" json:"forEach"` // changeImpacts
	Where   string      `yaml:"where,omitempty" json:"where,omitempty"`
	Produce ProduceSpec `yaml:"produce" json:"produce"`
	Link    *LinkSpec   `yaml:"link,omitempty" json:"link,omitempty"`
}

// ProduceSpec describes the produced node.
type ProduceSpec struct {
	Op       string `yaml:"op" json:"op"` // create_node | update_node
	NodeType string `yaml:"nodeType,omitempty" json:"nodeType,omitempty"`
}

// LinkSpec describes the expected link between the produced node and x.target.
type LinkSpec struct {
	Type      string `yaml:"type" json:"type"`
	Direction string `yaml:"direction,omitempty" json:"direction,omitempty"` // out (default) | in
}

// Expr compiles the expectation into a CEL expression. The iteration
// variable in Where is `x`.
func (e Expectation) Expr() (string, error) {
	if e.ForEach != "changeImpacts" {
		return "", fmt.Errorf("expects.forEach must be changeImpacts, got %q", e.ForEach)
	}
	// what is expected of a node the change modifies: created nodes are not concerned
	where := `x.intent == "modified"`
	if strings.TrimSpace(e.Where) != "" {
		where += " && (" + e.Where + ")"
	}
	src := fmt.Sprintf("changeImpacts.filter(x, %s)", where)
	match, err := e.changeImpactMatch()
	if err != nil {
		return "", err
	}
	// An expectation over an empty selection is vacuously true; require at
	// least one element so the effect is meaningful for the planner.
	return fmt.Sprintf("size(%s) > 0 && %s.all(x, %s)", src, src, match), nil
}

// changeImpactMatch is the match of an expectation over change impacts: what the change
// must contain for each x (a change impact with a pre version).
func (e Expectation) changeImpactMatch() (string, error) {
	switch e.Produce.Op {
	case "update_node":
		return `changeImpacts.exists(n, n.intent == "modified" && n.hasPost && n.key == x.key)`, nil
	case "create_node":
		if e.Link == nil || e.Link.Type == "" {
			return "", fmt.Errorf("expects: create_node requires link.type")
		}
		typeCheck := ""
		if e.Produce.NodeType != "" {
			typeCheck = fmt.Sprintf(" && n.type == %s", strconv.Quote(e.Produce.NodeType))
		}
		// "out": the new node links to x, "in": x links to the new node
		if e.Link.Direction == "in" {
			return fmt.Sprintf(`changeImpacts.exists(n, n.intent == "created" && n.hasPost%s && x.hasPost && x.post.out.exists(l, l.type == %s && l.to.id == n.post.id))`,
				typeCheck, strconv.Quote(e.Link.Type)), nil
		}
		return fmt.Sprintf(`changeImpacts.exists(n, n.intent == "created" && n.hasPost%s && n.post.out.exists(l, l.type == %s && l.to.id == x.pre.id))`,
			typeCheck, strconv.Quote(e.Link.Type)), nil
	}
	return "", fmt.Errorf("expects.produce.op must be create_node or update_node, got %q", e.Produce.Op)
}
