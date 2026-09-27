package graphsvc

import (
	"context"
	"errors"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
)

// alm is the namespace of the delivery domain (domains/alm.yaml).
const alm = "alm"

// SeedDemo loads a small ALM repository (namespace alm) once: needs, requirements and tests
// (methodologies/examples/impact-analysis.yaml), and the functions, components, build artifacts, applications,
// solution, data, interfaces and flows of methodologies/sdlc.yaml, with a small organisation owning them.
func SeedDemo(ctx context.Context, g *graph.Graph) (bool, error) {
	if _, err := g.NodeByKey(ctx, alm, "NEED-1"); err == nil {
		return false, nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return false, err
	}
	nodes := []graph.NewNode{
		{Namespace: alm, Key: "NEED-1", Type: alm + "@Need", Properties: map[string]any{"title": "Pay for orders online"}},
		{Namespace: alm, Key: "NEED-2", Type: alm + "@Need", Properties: map[string]any{"title": "Be refunded quickly"}},
		{Namespace: alm, Key: "REQ-1", Type: alm + "@Requirement", Properties: map[string]any{"title": "Card payment goes through the Acme PSP (API v1)", "priority": "high"}},
		{Namespace: alm, Key: "REQ-2", Type: alm + "@Requirement", Properties: map[string]any{"title": "The refund is initiated within 24h via the PSP", "priority": "medium"}},
		{Namespace: alm, Key: "REQ-3", Type: alm + "@Requirement", Properties: map[string]any{"title": "Receipts are sent by email", "priority": "low"}},
		{Namespace: alm, Key: "TST-1", Type: alm + "@TestCase", Properties: map[string]any{"title": "Nominal card payment"}},
		{Namespace: alm, Key: "TST-2", Type: alm + "@TestCase", Properties: map[string]any{"title": "Full refund"}},
		{Namespace: alm, Key: "TST-3", Type: alm + "@TestCase", Properties: map[string]any{"title": "Receipt received"}},
		{Namespace: alm, Key: "REQ-4", Type: alm + "@SecurityRequirement", Properties: map[string]any{"title": "Card data is never stored in the clear (PCI DSS)", "priority": "high"}},
		{Namespace: alm, Key: "CMP-1", Type: alm + "@Component", Properties: map[string]any{"title": "payment-service", "technology": "java", "version": "1.4.2"}},
		{Namespace: alm, Key: "CMP-2", Type: alm + "@Component", Properties: map[string]any{"title": "notification-service", "technology": "go", "version": "2.1.0"}},
		{Namespace: alm, Key: "CMP-3", Type: alm + "@Component", Properties: map[string]any{"title": "settlement-batch", "technology": "shell", "version": "0.9.3"}},
		{Namespace: alm, Key: "CMP-4", Type: alm + "@Component", Properties: map[string]any{"title": "card-crypto", "technology": "c", "version": "3.0.1"}},
		// ALM chain (alm)
		{Namespace: alm, Key: "FCT-1", Type: alm + "@Function", Properties: map[string]any{"title": "Collect a payment"}},
		{Namespace: alm, Key: "FCT-2", Type: alm + "@Function", Properties: map[string]any{"title": "Refund an order"}},
		{Namespace: alm, Key: "FCT-3", Type: alm + "@Function", Properties: map[string]any{"title": "Notify the customer"}},
		{Namespace: alm, Key: "FCT-4", Type: alm + "@Function", Properties: map[string]any{"title": "Protect card data"}},
		{Namespace: alm, Key: "ART-1", Type: alm + "@BuildArtifact", Properties: map[string]any{"name": "payment-service", "format": "jar", "version": "1.4.2", "coordinates": "com.acme:payment-service:1.4.2"}},
		{Namespace: alm, Key: "ART-2", Type: alm + "@BuildArtifact", Properties: map[string]any{"name": "notification-service", "format": "tar.gz", "version": "2.1.0"}},
		{Namespace: alm, Key: "ART-3", Type: alm + "@BuildArtifact", Properties: map[string]any{"name": "settlement-batch", "format": "tar.gz", "version": "0.9.3"}},
		{Namespace: alm, Key: "ART-4", Type: alm + "@BuildArtifact", Properties: map[string]any{"name": "card-crypto", "format": "elf", "version": "3.0.1"}},
		{Namespace: alm, Key: "APP-1", Type: alm + "@Application", Properties: map[string]any{"title": "Checkout", "version": "5.2"}},
		{Namespace: alm, Key: "APP-2", Type: alm + "@Application", Properties: map[string]any{"title": "CRM", "version": "3.0"}},
		{Namespace: alm, Key: "APP-3", Type: alm + "@Application", Properties: map[string]any{"title": "Finance back office", "version": "1.7"}},
		{Namespace: alm, Key: "SOL-1", Type: alm + "@Solution", Properties: map[string]any{"title": "Online commerce"}},
		{Namespace: alm, Key: "DAT-1", Type: alm + "@Data", Properties: map[string]any{"title": "Order", "classification": "internal"}},
		{Namespace: alm, Key: "DAT-2", Type: alm + "@Data", Properties: map[string]any{"title": "Payment", "classification": "confidential"}},
		{Namespace: alm, Key: "DAT-3", Type: alm + "@Data", Properties: map[string]any{"title": "Customer", "classification": "personal"}},
		{Namespace: alm, Key: "ITF-1", Type: alm + "@Interface", Properties: map[string]any{"title": "Payments API", "protocol": "REST", "version": "v1"}},
		{Namespace: alm, Key: "ITF-2", Type: alm + "@Interface", Properties: map[string]any{"title": "Notification events", "protocol": "Kafka", "version": "v2"}},
		{Namespace: alm, Key: "ITF-3", Type: alm + "@Interface", Properties: map[string]any{"title": "Settlement file", "protocol": "SFTP", "version": "v1"}},
		{Namespace: alm, Key: "FLW-1", Type: alm + "@Flow", Properties: map[string]any{"title": "Payment confirmation", "mode": "asynchronous", "frequency": "real time"}},
		{Namespace: alm, Key: "FLW-2", Type: alm + "@Flow", Properties: map[string]any{"title": "Daily settlement", "mode": "batch", "frequency": "daily"}},
		// environments and releases
		{Namespace: alm, Key: "ENV-DEV", Type: alm + "@Environment", Properties: map[string]any{"name": "Development", "stage": "dev", "order": 1}},
		{Namespace: alm, Key: "ENV-TEST", Type: alm + "@Environment", Properties: map[string]any{"name": "Integration", "stage": "test", "order": 2}},
		{Namespace: alm, Key: "ENV-STG", Type: alm + "@Environment", Properties: map[string]any{"name": "Staging", "stage": "staging", "order": 3}},
		{Namespace: alm, Key: "ENV-PRD", Type: alm + "@Environment", Properties: map[string]any{"name": "Production", "stage": "prod", "order": 4}},
		{Namespace: alm, Key: "REL-APP-1-5.2", Type: alm + "@Release", Properties: map[string]any{"title": "Checkout 5.2", "version": "5.2", "status": "deployed"}},
		{Namespace: alm, Key: "DEP-REL-APP-1-5.2-ENV-PRD", Type: alm + "@Deployment", Properties: map[string]any{"status": "succeeded", "stage": "prod"}},
	}
	// organisation namespace: the units that own the nodes above
	orgUnit := func(key, name, kind string) graph.NewNode {
		return graph.NewNode{Namespace: mcp.NamespaceOrganisation, Key: key, Type: mcp.NodeTypeOrgUnit, Properties: map[string]any{"name": name, "kind": kind}}
	}
	nodes = append(nodes,
		orgUnit("ORG-ACME", "Acme", "company"),
		orgUnit("ORG-DIGITAL", "Direction Digitale", "direction"),
		orgUnit("ORG-CHECKOUT", "Team Checkout", "team"),
		orgUnit("ORG-CRM", "Team CRM", "team"),
		orgUnit("ORG-FINANCE", "Team Finance", "team"),
		orgUnit("ORG-SECURITY", "Team Security", "team"),
	)
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
		// organisation hierarchy (child part_of parent)
		{"ORG-DIGITAL", "part_of", "ORG-ACME"},
		{"ORG-CHECKOUT", "part_of", "ORG-DIGITAL"}, {"ORG-CRM", "part_of", "ORG-DIGITAL"},
		{"ORG-FINANCE", "part_of", "ORG-DIGITAL"}, {"ORG-SECURITY", "part_of", "ORG-DIGITAL"},
		// ownership: a node of one namespace references a unit of another
		{"CMP-1", "owner", "ORG-CHECKOUT"}, {"CMP-2", "owner", "ORG-CRM"}, {"CMP-3", "owner", "ORG-FINANCE"}, {"CMP-4", "owner", "ORG-SECURITY"},
		{"APP-1", "owner", "ORG-CHECKOUT"}, {"APP-2", "owner", "ORG-CRM"}, {"APP-3", "owner", "ORG-FINANCE"},
		{"SOL-1", "owner", "ORG-DIGITAL"},
	}
	for _, l := range links {
		typ := alm + "@" + l[1]
		if l[1] == "part_of" || l[1] == "owner" {
			typ = mcp.NamespaceOrganisation + "@" + l[1]
		}
		if _, err := g.Link(ctx, typ, refs[l[0]], refs[l[2]], nil); err != nil {
			return false, err
		}
	}
	if _, err := g.CreateBaselineFromLatest(ctx, alm, "Initial baseline"); err != nil {
		return false, err
	}
	_, err := g.CreateBaselineFromLatest(ctx, mcp.NamespaceOrganisation, "Initial baseline")
	return err == nil, err
}
