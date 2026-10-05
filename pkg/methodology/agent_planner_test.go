package methodology

import (
	"github.com/zimwip/goap/pkg/domain/def"
	"strings"
	"testing"
)

func TestAgentModelRequiredForLLMPlanners(t *testing.T) {
	for _, planner := range []string{PlannerLLM, PlannerLLMScoring} {
		m := refMethodology()
		m.Agents = []Agent{{Name: "a", Planner: planner}}
		issues := m.Resolve(def.DomainTypes(testDomain())).Validate()
		if len(issues) != 1 || issues[0].Path != "agents[0].model" {
			t.Fatalf("%s planner without model: %v", planner, issues)
		}
		m.Agents[0].Model = "fast"
		if issues := m.Resolve(def.DomainTypes(testDomain())).Validate(); len(issues) > 0 {
			t.Fatalf("%s planner with model: %v", planner, issues)
		}
	}
}

func TestUnknownPlannerMessageListsAllFive(t *testing.T) {
	m := refMethodology()
	m.Agents = []Agent{{Name: "a", Planner: "bogus"}}
	issues := m.Resolve(def.DomainTypes(testDomain())).Validate()
	if len(issues) != 1 || issues[0].Path != "agents[0].planner" {
		t.Fatalf("issues: %v", issues)
	}
	for _, want := range []string{"goap", "utility", "hybrid", "llm", "llm-scoring"} {
		if !strings.Contains(issues[0].Message, want) {
			t.Errorf("message %q missing %q", issues[0].Message, want)
		}
	}
}
