package registrysvc

import (
	"reflect"
	"testing"

	"github.com/zimwip/goap/pkg/methodology"
)

func TestAgentModelRoundTripsThroughPB(t *testing.T) {
	r := Record{Methodology: methodology.Methodology{
		Name: "m", Version: "1",
		Agents: []methodology.Agent{{Name: "a", Planner: methodology.PlannerLLMScoring, Model: "fast"}},
	}}
	p := ToPB(r)
	if len(p.Agents) != 1 || p.Agents[0].Model != "fast" {
		t.Fatalf("ToPB dropped Agent.Model: %+v", p.Agents)
	}
	m := FromPB(p)
	if len(m.Agents) != 1 || m.Agents[0].Model != "fast" {
		t.Fatalf("FromPB dropped Agent.Model: %+v", m.Agents)
	}
}

func TestProcessesRoundTripThroughPB(t *testing.T) {
	in := methodology.Methodology{Name: "m", Version: "1", Processes: []methodology.Process{{Name: "flow", Description: "d",
		References: []methodology.Reference{{Title: "Delivery guide", Ref: "document-repository:procedures/delivery.md", Section: "§2"}}, Steps: []methodology.Step{
			{Name: "phase", Steps: []methodology.Step{{Name: "a", Action: "do", Pre: map[string]bool{"x": true}}, {Name: "b", Agent: "ag", Goal: "g", References: []methodology.Reference{{Ref: "doc:SAD-1"}}}}},
			{Name: "nested", Process: "other/p", Done: map[string]bool{"y": true}},
			{Name: "sign", Instructions: "sign it", Guidance: "how", Checklist: []string{"one"}, Deliverables: []string{"Report"}},
			{Name: "alt", Actions: []string{"x", "y"}},
			{Name: "cap", Capability: "design", Roles: &methodology.Responsibilities{Responsible: "dev", Consulted: []string{"arch"}}},
		}}}}
	out := FromPB(ToPB(Record{Methodology: in}))
	in.Methods = []methodology.Method{{Name: "m", For: "design", When: "true", Priority: 3, Guidance: "g", Checklist: []string{"c"},
		Deliverables: []string{"D"}, References: []methodology.Reference{{Ref: "doc:X"}}, Agent: "a", Goal: "g",
		Roles: &methodology.Responsibilities{Accountable: "lead", Informed: []string{"po"}}}}
	in.Roles = []methodology.Role{{Name: "dev", Description: "d"}, {Name: "lead"}}
	in.AppliesTo = []string{"sdlc"}
	in.On = []methodology.Subscription{{Event: "step.completed", Filter: `event.step.process == "x"`}}
	out = FromPB(ToPB(Record{Methodology: in}))
	if !reflect.DeepEqual(out.Methods, in.Methods) || !reflect.DeepEqual(out.Roles, in.Roles) || !reflect.DeepEqual(out.AppliesTo, in.AppliesTo) || !reflect.DeepEqual(out.On, in.On) {
		t.Fatalf("methods changed through PB:\n%+v\n%+v", in.Methods, out.Methods)
	}
	if !reflect.DeepEqual(out.Processes, in.Processes) {
		t.Fatalf("processes changed through PB:\n%+v\n%+v", in.Processes, out.Processes)
	}
	s := SummaryToPB(Record{Methodology: in})
	if len(s.Agents) != 1 || s.Agents[0].Name != "flow" || s.Agents[0].Planner != "process" {
		t.Fatalf("a process is listed as the agent that runs it: %+v", s.Agents)
	}
}
