package registrysvc

import (
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
