package registrysvc

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/methodology"
)

func algorithmsToPB(as []algo.Algorithm) []*registryv1.Algorithm {
	var out []*registryv1.Algorithm
	for _, a := range as {
		pa := &registryv1.Algorithm{Name: a.Name, Description: a.Description, Type: string(a.Type), Language: a.Language, Code: a.Code, Mcp: a.MCP, Connector: a.Connector}
		for _, p := range a.Params {
			pp := &registryv1.AlgorithmParam{Name: p.Name, Type: p.Type, Description: p.Description, Required: p.Required, Values: p.Values}
			if p.Default != nil {
				pp.DefaultValue, _ = structpb.NewValue(normalizeJSON(p.Default))
			}
			pa.Params = append(pa.Params, pp)
		}
		out = append(out, pa)
	}
	return out
}

func algorithmsFromPB(as []*registryv1.Algorithm) []algo.Algorithm {
	var out []algo.Algorithm
	for _, a := range as {
		da := algo.Algorithm{Name: a.Name, Description: a.Description, Type: algo.Usage(a.Type), Language: a.Language, Code: a.Code, MCP: a.Mcp, Connector: a.Connector}
		for _, p := range a.Params {
			dp := algo.Param{Name: p.Name, Type: p.Type, Description: p.Description, Required: p.Required, Values: nilIfNone(p.Values)}
			if p.DefaultValue != nil {
				dp.Default = p.DefaultValue.AsInterface()
			}
			da.Params = append(da.Params, dp)
		}
		out = append(out, da)
	}
	return out
}

func instancesToPB(is []algo.Instance) []*registryv1.AlgorithmInstance {
	var out []*registryv1.AlgorithmInstance
	for _, i := range is {
		out = append(out, &registryv1.AlgorithmInstance{Name: i.Name, Description: i.Description, Algorithm: i.Algorithm, Values: pbconv.Struct(i.Values)})
	}
	return out
}

func instancesFromPB(is []*registryv1.AlgorithmInstance) []algo.Instance {
	var out []algo.Instance
	for _, i := range is {
		out = append(out, algo.Instance{Name: i.Name, Description: i.Description, Algorithm: i.Algorithm, Values: pbconv.Map(i.Values)})
	}
	return out
}

func validatorsToPB(vs []methodology.PropertyValidator) []*registryv1.PropertyValidator {
	var out []*registryv1.PropertyValidator
	for _, v := range vs {
		out = append(out, &registryv1.PropertyValidator{Property: v.Property, Instance: v.Instance})
	}
	return out
}

func validatorsFromPB(vs []*registryv1.PropertyValidator) []methodology.PropertyValidator {
	var out []methodology.PropertyValidator
	for _, v := range vs {
		out = append(out, methodology.PropertyValidator{Property: v.Property, Instance: v.Instance})
	}
	return out
}

func normalizeJSON(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// RunAlgorithm tries an algorithm on a sample input (nothing is stored). It needs
// the right to edit domains.
func (s *Service) RunAlgorithm(ctx context.Context, a algo.Algorithm, values map[string]any, input map[string]any) (dsl.Outcome, error) {
	if err := s.authorizeDomain(ctx, "write", &methodology.Domain{}); err != nil {
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
	b := algo.Bound{Instance: "try", Algorithm: a.Name, Type: a.Type, Language: a.Language, Code: a.Code, Params: params}
	return dsl.RunAlgorithm(ctx, b, dsl.AlgorithmInput{Node: sample.Node, Property: sample.Property, Value: sample.Value,
		Children: sample.Children, Change: sample.Change, Transition: sample.Transition})
}
