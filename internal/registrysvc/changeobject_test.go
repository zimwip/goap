package registrysvc

import (
	"reflect"
	"testing"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain/def"
)

const risksDomainYAML = `
name: risks
version: "1"
enums: [{name: level, values: [low, high]}]
lifecycles:
  - name: risk
    initial: open
    states: [{name: open}, {name: closed, final: true}]
    transitions: [{name: close, from: open, to: closed}]
changeObjectTypes:
  - name: Risk
    key: {kind: sequence, prefix: RISK}
    lifecycle: risk
    editor: risk-register
    attributes: [{name: title, asName: true}, {name: level, type: enum, enum: level}]
    search: [{property: title, text: true}]
  - {name: Mitigation, key: {kind: ref, ref: Risk}, scope: workspace, additionalProperties: true}
  - {name: Owner, key: {kind: natural, attributes: [role]}, attributes: [role]}
`

// A domain keeps its change object types and its enums through the RPC conversions (ADR 0098): the graph and the engine
// load the domains through the registry client.
func TestChangeObjectTypesRoundTripThroughPB(t *testing.T) {
	d, err := def.ParseDomain([]byte(risksDomainYAML))
	if err != nil {
		t.Fatal(err)
	}
	p := DomainToPB(DomainRecord{Domain: *d})
	if len(p.ChangeObjectTypes) != 3 || len(p.Enums) != 1 {
		t.Fatalf("ToPB: %+v", p)
	}
	back := DomainFromPB(p)
	if !reflect.DeepEqual(back.ChangeObjectTypes, d.ChangeObjectTypes) || !reflect.DeepEqual(back.Enums, d.Enums) {
		t.Fatalf("round trip:\n%+v\n%+v", back.ChangeObjectTypes, d.ChangeObjectTypes)
	}
	if s := DomainSummaryToPB(DomainRecord{Domain: *d}); s.ChangeObjectTypeCount != 3 {
		t.Fatalf("summary: %+v", s)
	}
}

// ListTypes serves the resolved change object types, the built-in execution ones included.
func TestListTypesChangeObjectTypes(t *testing.T) {
	enf, _ := authz.NewCasbin(nil)
	s := &Service{Store: NewMemoryStore(), Authz: enf}
	if _, issues, err := s.ImportDomain(as("admin"), []byte(risksDomainYAML), true); err != nil || issues.HasErrors() {
		t.Fatalf("import: %v %v", issues, err)
	}
	res, err := (&Handler{Service: s}).ListTypes(as("contributor"), connect.NewRequest(&registryv1.ListTypesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	byRef := map[string]*registryv1.ChangeObjectTypeInfo{}
	for _, o := range res.Msg.ChangeObjectTypes {
		byRef[o.Ref] = o
	}
	r := byRef["risks@Risk"]
	if r == nil || r.Key.GetKind() != def.KeySequence || r.Key.GetPrefix() != "RISK" || r.Lifecycle.GetName() != "risk" || r.Editor != "risk-register" ||
		r.Scope != def.ScopeChange || len(r.Attributes) != 2 || len(r.Attributes[1].Values) != 2 {
		t.Fatalf("risk: %+v", r)
	}
	if m := byRef["risks@Mitigation"]; m == nil || m.Key.GetRef() != "risks@Risk" || m.Scope != def.ScopeWorkspace || !m.AdditionalProperties {
		t.Fatalf("mitigation: %+v", m)
	}
	if byRef["execution@Methodology"].GetKey().GetKind() != def.KeyNatural || byRef["execution@Run"].GetKey().GetRef() != def.RefRun {
		t.Fatalf("the built-in execution types are listed: %v", byRef)
	}
}
