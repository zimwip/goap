package domainsvc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zimwip/goap/contracts/algo"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/dsl"
)

// RunAlgorithm tries an algorithm on a sample input (nothing is stored). It needs
// the right to edit domains.
func (s *Service) RunAlgorithm(ctx context.Context, a algo.Algorithm, values map[string]any, input map[string]any) (dsl.Outcome, error) {
	if err := s.authorizeDomain(ctx, "write", &def.Domain{}); err != nil {
		return dsl.Outcome{}, err
	}
	if issues := a.Issues(); len(issues) > 0 {
		return dsl.Outcome{}, fmt.Errorf("%w: %s", ErrInvalid, issues[0])
	}
	if err := dsl.CheckAlgorithmCode(a.Language, a.Code); err != nil {
		return dsl.Outcome{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	params, issues := a.Resolve(values)
	if len(issues) > 0 {
		return dsl.Outcome{}, fmt.Errorf("%w: %s", ErrInvalid, issues[0])
	}
	var sample struct {
		Node       dsl.Node           `json:"node"`
		Property   string             `json:"property"`
		Value      any                `json:"value"`
		Link       dsl.LinkInfo       `json:"link"`
		From       dsl.Node           `json:"from"`
		To         dsl.Node           `json:"to"`
		Children   []dsl.Node         `json:"children"`
		Transition dsl.TransitionInfo `json:"transition"`
		Change     dsl.ChangeInfo     `json:"change"`
	}
	raw, _ := json.Marshal(input)
	if err := json.Unmarshal(raw, &sample); err != nil {
		return dsl.Outcome{}, fmt.Errorf("%w: sample input: %v", ErrInvalid, err)
	}
	if sample.Node.Props == nil {
		sample.Node.Props = map[string]any{}
	}
	for _, n := range []*dsl.Node{&sample.From, &sample.To} {
		if n.Props == nil {
			n.Props = map[string]any{}
		}
	}
	if sample.Link.Props == nil {
		sample.Link.Props = map[string]any{}
	}
	b := algo.Bound{Instance: "try", Algorithm: a.Name, Type: a.Type, Language: a.Language, Code: a.Code, Params: params}
	return dsl.RunAlgorithm(ctx, b, dsl.AlgorithmInput{Node: sample.Node, Property: sample.Property, Value: sample.Value, Link: sample.Link, From: sample.From, To: sample.To,
		Children: sample.Children, Change: sample.Change, Transition: sample.Transition})
}
