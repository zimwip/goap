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
// For each element x of ForEach (filtered by Where), the change must contain
// a proposal producing a node of type Produce.NodeType, either:
//   - Produce.Op == "update_node": a new version of x.target;
//   - Produce.Op == "create_node": a new node linked to x.target by Link.Type
//     (direction "out": new -> target, "in": target -> new).
type Expectation struct {
	ForEach string      `yaml:"forEach" json:"forEach"` // impacts | proposals | items | artifacts
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
	switch e.ForEach {
	case "impacts", "proposals", "items", "artifacts":
	default:
		return "", fmt.Errorf("expects.forEach must be impacts, proposals, items or artifacts, got %q", e.ForEach)
	}
	src := e.ForEach
	if strings.TrimSpace(e.Where) != "" {
		src = fmt.Sprintf("%s.filter(x, %s)", e.ForEach, e.Where)
	}
	var match string
	switch e.Produce.Op {
	case "update_node":
		match = `proposals.exists(p, p.op == "update_node" && p.node.base.id == x.target.id)`
	case "create_node":
		if e.Link == nil || e.Link.Type == "" {
			return "", fmt.Errorf("expects: create_node requires link.type")
		}
		self, other := "from", "to"
		if e.Link.Direction == "in" {
			self, other = "to", "from"
		}
		typeCheck := ""
		if e.Produce.NodeType != "" {
			typeCheck = fmt.Sprintf(" && p.link.%s.type == %s", self, strconv.Quote(e.Produce.NodeType))
		}
		match = fmt.Sprintf(`proposals.exists(p, p.op == "add_link" && p.link.type == %s && p.link.%s.item != "" && p.link.%s.id == x.target.id%s)`,
			strconv.Quote(e.Link.Type), self, other, typeCheck)
	default:
		return "", fmt.Errorf("expects.produce.op must be create_node or update_node, got %q", e.Produce.Op)
	}
	// An expectation over an empty selection is vacuously true; require at
	// least one element so the effect is meaningful for the planner.
	return fmt.Sprintf("size(%s) > 0 && %s.all(x, %s)", src, src, match), nil
}
