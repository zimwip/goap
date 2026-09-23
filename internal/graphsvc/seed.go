package graphsvc

import (
	"context"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// SeedDemo loads a small ALM repository when the graph is empty: needs,
// requirements and tests (methodologies/impact-analysis.yaml), and the
// functions, components, build artifacts, applications, solution, data,
// interfaces and flows of methodologies/sdlc.yaml.
func SeedDemo(ctx context.Context, g *graph.Graph) (bool, error) {
	bs, err := g.Baselines(ctx)
	if err != nil || len(bs) > 0 {
		return false, err
	}
	nodes := []graph.NewNode{
		{Key: "NEED-1", Type: "Need", Properties: map[string]any{"title": "Pay for orders online"}},
		{Key: "NEED-2", Type: "Need", Properties: map[string]any{"title": "Be refunded quickly"}},
		{Key: "REQ-1", Type: "Requirement", Properties: map[string]any{"title": "Card payment goes through the Acme PSP (API v1)", "priority": "high"}},
		{Key: "REQ-2", Type: "Requirement", Properties: map[string]any{"title": "The refund is initiated within 24h via the PSP", "priority": "medium"}},
		{Key: "REQ-3", Type: "Requirement", Properties: map[string]any{"title": "Receipts are sent by email", "priority": "low"}},
		{Key: "TST-1", Type: "TestCase", Properties: map[string]any{"title": "Nominal card payment"}},
		{Key: "TST-2", Type: "TestCase", Properties: map[string]any{"title": "Full refund"}},
		{Key: "TST-3", Type: "TestCase", Properties: map[string]any{"title": "Receipt received"}},
		{Key: "REQ-4", Type: "SecurityRequirement", Properties: map[string]any{"title": "Card data is never stored in the clear (PCI DSS)", "priority": "high"}},
		{Key: "CMP-1", Type: "Component", Properties: map[string]any{"title": "payment-service", "owner": "team-checkout", "technology": "java", "version": "1.4.2"}},
		{Key: "CMP-2", Type: "Component", Properties: map[string]any{"title": "notification-service", "owner": "team-crm", "technology": "go", "version": "2.1.0"}},
		{Key: "CMP-3", Type: "Component", Properties: map[string]any{"title": "settlement-batch", "owner": "team-finance", "technology": "shell", "version": "0.9.3"}},
		{Key: "CMP-4", Type: "Component", Properties: map[string]any{"title": "card-crypto", "owner": "team-security", "technology": "c", "version": "3.0.1"}},
		// ALM chain (sdlc)
		{Key: "FCT-1", Type: "Function", Properties: map[string]any{"title": "Collect a payment"}},
		{Key: "FCT-2", Type: "Function", Properties: map[string]any{"title": "Refund an order"}},
		{Key: "FCT-3", Type: "Function", Properties: map[string]any{"title": "Notify the customer"}},
		{Key: "FCT-4", Type: "Function", Properties: map[string]any{"title": "Protect card data"}},
		{Key: "ART-1", Type: "BuildArtifact", Properties: map[string]any{"name": "payment-service", "format": "jar", "version": "1.4.2", "coordinates": "com.acme:payment-service:1.4.2"}},
		{Key: "ART-2", Type: "BuildArtifact", Properties: map[string]any{"name": "notification-service", "format": "tar.gz", "version": "2.1.0"}},
		{Key: "ART-3", Type: "BuildArtifact", Properties: map[string]any{"name": "settlement-batch", "format": "tar.gz", "version": "0.9.3"}},
		{Key: "ART-4", Type: "BuildArtifact", Properties: map[string]any{"name": "card-crypto", "format": "elf", "version": "3.0.1"}},
		{Key: "APP-1", Type: "Application", Properties: map[string]any{"title": "Checkout", "owner": "team-checkout", "version": "5.2"}},
		{Key: "APP-2", Type: "Application", Properties: map[string]any{"title": "CRM", "owner": "team-crm", "version": "3.0"}},
		{Key: "APP-3", Type: "Application", Properties: map[string]any{"title": "Finance back office", "owner": "team-finance", "version": "1.7"}},
		{Key: "SOL-1", Type: "Solution", Properties: map[string]any{"title": "Online commerce", "owner": "direction-digitale"}},
		{Key: "DAT-1", Type: "Data", Properties: map[string]any{"title": "Order", "classification": "internal"}},
		{Key: "DAT-2", Type: "Data", Properties: map[string]any{"title": "Payment", "classification": "confidential"}},
		{Key: "DAT-3", Type: "Data", Properties: map[string]any{"title": "Customer", "classification": "personal"}},
		{Key: "ITF-1", Type: "Interface", Properties: map[string]any{"title": "Payments API", "protocol": "REST", "version": "v1"}},
		{Key: "ITF-2", Type: "Interface", Properties: map[string]any{"title": "Notification events", "protocol": "Kafka", "version": "v2"}},
		{Key: "ITF-3", Type: "Interface", Properties: map[string]any{"title": "Settlement file", "protocol": "SFTP", "version": "v1"}},
		{Key: "FLW-1", Type: "Flow", Properties: map[string]any{"title": "Payment confirmation", "mode": "asynchronous", "frequency": "real time"}},
		{Key: "FLW-2", Type: "Flow", Properties: map[string]any{"title": "Daily settlement", "mode": "batch", "frequency": "daily"}},
		// environments and releases
		{Key: "ENV-DEV", Type: "Environment", Properties: map[string]any{"name": "Development", "stage": "dev", "order": 1}},
		{Key: "ENV-TEST", Type: "Environment", Properties: map[string]any{"name": "Integration", "stage": "test", "order": 2}},
		{Key: "ENV-STG", Type: "Environment", Properties: map[string]any{"name": "Staging", "stage": "staging", "order": 3}},
		{Key: "ENV-PRD", Type: "Environment", Properties: map[string]any{"name": "Production", "stage": "prod", "order": 4}},
		{Key: "REL-APP-1-5.2", Type: "Release", Properties: map[string]any{"title": "Checkout 5.2", "version": "5.2", "status": "deployed"}},
		{Key: "DEP-REL-APP-1-5.2-ENV-PRD", Type: "Deployment", Properties: map[string]any{"status": "succeeded", "stage": "prod"}},
	}
	refs := map[string]domain.NodeRef{}
	for _, n := range nodes {
		created, err := g.CreateNode(ctx, n)
		if err != nil {
			return false, err
		}
		refs[n.Key] = created.Ref()
	}
	links := [][3]string{
		{"REQ-1", "satisfies", "NEED-1"}, {"REQ-2", "satisfies", "NEED-2"}, {"REQ-3", "satisfies", "NEED-1"},
		{"TST-1", "verifies", "REQ-1"}, {"TST-2", "verifies", "REQ-2"}, {"TST-3", "verifies", "REQ-3"},
		{"CMP-1", "implements", "REQ-1"}, {"CMP-1", "implements", "REQ-2"}, {"CMP-2", "implements", "REQ-3"},
		// ALM chain: requirement ← function ← component ← artifact / application ← solution
		{"REQ-4", "satisfies", "NEED-1"},
		{"FCT-1", "realizes", "REQ-1"}, {"FCT-2", "realizes", "REQ-2"}, {"FCT-3", "realizes", "REQ-3"}, {"FCT-4", "realizes", "REQ-4"},
		{"CMP-1", "implements", "FCT-1"}, {"CMP-1", "implements", "FCT-2"}, {"CMP-2", "implements", "FCT-3"},
		{"CMP-3", "implements", "FCT-2"}, {"CMP-4", "implements", "FCT-4"},
		{"CMP-1", "depends_on", "CMP-4"}, {"CMP-1", "accesses", "DAT-2"}, {"CMP-3", "accesses", "DAT-2"}, {"CMP-2", "accesses", "DAT-3"},
		{"ART-1", "built_from", "CMP-1"}, {"ART-2", "built_from", "CMP-2"}, {"ART-3", "built_from", "CMP-3"}, {"ART-4", "built_from", "CMP-4"},
		{"APP-1", "composed_of", "CMP-1"}, {"APP-1", "composed_of", "CMP-4"}, {"APP-2", "composed_of", "CMP-2"}, {"APP-3", "composed_of", "CMP-3"},
		{"APP-1", "deploys", "ART-1"}, {"APP-1", "deploys", "ART-4"}, {"APP-2", "deploys", "ART-2"}, {"APP-3", "deploys", "ART-3"},
		{"SOL-1", "includes", "APP-1"}, {"SOL-1", "includes", "APP-2"}, {"SOL-1", "includes", "APP-3"},
		{"SOL-1", "addresses", "NEED-1"}, {"SOL-1", "addresses", "NEED-2"},
		{"DAT-1", "owned_by", "APP-1"}, {"DAT-2", "owned_by", "APP-1"}, {"DAT-3", "owned_by", "APP-2"},
		{"ITF-1", "exposed_by", "APP-1"}, {"ITF-1", "exchanges", "DAT-2"},
		{"ITF-2", "exposed_by", "APP-2"}, {"ITF-2", "exchanges", "DAT-3"},
		{"ITF-3", "exposed_by", "APP-3"}, {"ITF-3", "exchanges", "DAT-2"},
		{"FLW-1", "source", "APP-1"}, {"FLW-1", "target", "APP-2"}, {"FLW-1", "through", "ITF-2"}, {"FLW-1", "carries", "DAT-3"},
		{"FLW-2", "source", "APP-1"}, {"FLW-2", "target", "APP-3"}, {"FLW-2", "through", "ITF-3"}, {"FLW-2", "carries", "DAT-2"},
		{"ENV-DEV", "promotes_to", "ENV-TEST"}, {"ENV-TEST", "promotes_to", "ENV-STG"}, {"ENV-STG", "promotes_to", "ENV-PRD"},
		{"REL-APP-1-5.2", "releases", "APP-1"}, {"REL-APP-1-5.2", "contains", "ART-1"}, {"REL-APP-1-5.2", "contains", "ART-4"},
		{"DEP-REL-APP-1-5.2-ENV-PRD", "of_release", "REL-APP-1-5.2"}, {"DEP-REL-APP-1-5.2-ENV-PRD", "in_environment", "ENV-PRD"},
	}
	for _, l := range links {
		if _, err := g.Link(ctx, l[1], refs[l[0]], refs[l[2]], nil); err != nil {
			return false, err
		}
	}
	_, err = g.CreateBaselineFromLatest(ctx, "Initial baseline")
	return err == nil, err
}
