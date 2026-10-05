// Package devseed holds the data of the development and demo compositions: the ALM demo repository and the local file
// system document repository. It is separate from the platform bootstrap (graphsvc.Boot), which every composition runs;
// a production composition never calls it unless asked (GOAP_GRAPH_SEED=demo), the dev composition (goap-dev) does.
package devseed

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// alm is the namespace of the delivery domain (domains/alm.yaml).
const alm = "alm"

// demoNode is a node of the demo repository.
type demoNode struct {
	Namespace, Key, Type string
	Properties           map[string]any
	// State is the state a node of a lifecycle lands in: a node is created in the initial state, which no
	// change leaves a node in if it is not landable.
	State string
}

// Demo loads a small ALM repository (namespace alm) once: needs, requirements and tests
// (methodologies/examples/impact-analysis.yaml), and the functions, components, build artifacts, applications,
// solution, data, interfaces and flows of methodologies/sdlc.yaml, with a small organisation owning them. The graph is
// bootstrapped first (a seed is an ordinary change, ADR 0054).
func Demo(ctx context.Context, g *graph.Graph) (bool, error) {
	if err := g.Bootstrap(ctx); err != nil {
		return false, err
	}
	if _, err := g.NodeByKey(ctx, alm, "NEED-1"); err == nil {
		return false, nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return false, err
	}
	// the organisation first, under the root unit of the bootstrap (ADR 0054): the nodes below are owned by its units.
	// All the units are one change; a unit links to its parent, created by the same change (ToKey) or the root.
	root, err := g.NodeByKey(ctx, access.NamespaceOrganisation, g.Structure(domain.StructureOrganisation).Root)
	if err != nil {
		return false, err
	}
	var units []graph.NodeEdit
	for _, u := range [][4]string{
		{"ORG-ACME", "Acme", "company", ""}, {"ORG-DIGITAL", "Direction Digitale", "direction", "ORG-ACME"},
		{"ORG-CHECKOUT", "Team Checkout", "team", "ORG-DIGITAL"}, {"ORG-CRM", "Team CRM", "team", "ORG-DIGITAL"},
		{"ORG-FINANCE", "Team Finance", "team", "ORG-DIGITAL"}, {"ORG-SECURITY", "Team Security", "team", "ORG-DIGITAL"},
	} {
		e := graphsvc.SeedNode(u[0], access.NodeTypeOrgUnit, map[string]any{"name": u[1], "kind": u[2]})
		if u[3] == "" {
			e = linkPartOf(e, root.Ref())
		} else {
			e.Links = append(e.Links, graph.LinkEdit{Type: access.LinkPartOf, ToKey: u[3]})
		}
		units = append(units, e)
	}
	if err := graphsvc.SeedChange(ctx, g, access.NamespaceOrganisation, "Import demo organisation", units); err != nil {
		return false, err
	}
	// ownership: a node of one namespace is owned by a unit of the organisation (the owner of its versions)
	owners := map[string]string{"CMP-1": "ORG-CHECKOUT", "CMP-2": "ORG-CRM", "CMP-3": "ORG-FINANCE", "CMP-4": "ORG-SECURITY",
		"APP-1": "ORG-CHECKOUT", "APP-2": "ORG-CRM", "APP-3": "ORG-FINANCE", "SOL-1": "ORG-DIGITAL"}
	nodes := []demoNode{
		{Namespace: alm, Key: "NEED-1", Type: alm + "@Need", Properties: map[string]any{"title": "Pay for orders online"}},
		{Namespace: alm, Key: "NEED-2", Type: alm + "@Need", Properties: map[string]any{"title": "Be refunded quickly"}},
		{Namespace: alm, Key: "REQ-1", Type: alm + "@Requirement", Properties: map[string]any{"title": "Card payment goes through the Acme PSP (API v1)", "priority": "high"}, State: "proposed"},
		{Namespace: alm, Key: "REQ-2", Type: alm + "@Requirement", Properties: map[string]any{"title": "The refund is initiated within 24h via the PSP", "priority": "medium"}, State: "proposed"},
		{Namespace: alm, Key: "REQ-3", Type: alm + "@Requirement", Properties: map[string]any{"title": "Receipts are sent by email", "priority": "low"}, State: "proposed"},
		{Namespace: alm, Key: "TST-1", Type: alm + "@TestCase", Properties: map[string]any{"title": "Nominal card payment"}},
		{Namespace: alm, Key: "TST-2", Type: alm + "@TestCase", Properties: map[string]any{"title": "Full refund"}},
		{Namespace: alm, Key: "TST-3", Type: alm + "@TestCase", Properties: map[string]any{"title": "Receipt received"}},
		{Namespace: alm, Key: "REQ-4", Type: alm + "@SecurityRequirement", Properties: map[string]any{"title": "Card data is never stored in the clear (PCI DSS)", "priority": "high"}, State: "proposed"},
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
		{Namespace: alm, Key: "REL-APP-1-5.2", Type: alm + "@Release", Properties: map[string]any{"title": "Checkout 5.2", "version": "5.2", "status": "deployed"}, State: "candidate"},
		{Namespace: alm, Key: "DEP-REL-APP-1-5.2-ENV-PRD", Type: alm + "@Deployment", Properties: map[string]any{"status": "succeeded", "stage": "prod"}},
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
	}
	// the nodes and their links are one change (a link belongs to the version of its source, created here)
	edits := make([]graph.NodeEdit, 0, len(nodes))
	for _, n := range nodes {
		e := graphsvc.SeedNode(n.Key, n.Type, n.Properties)
		e.Owner, e.State = owners[n.Key], n.State
		edits = append(edits, e)
	}
	// the nodes of a change link each other, cycles included (an application is composed of components that
	// access data it owns): the links are part of the version of their source
	at := make(map[string]int, len(nodes))
	for i, n := range nodes {
		at[n.Key] = i
	}
	for _, l := range links {
		i, ok := at[l[0]]
		if !ok {
			return false, fmt.Errorf("demo link from unknown node %s", l[0])
		}
		edits[i].Links = append(edits[i].Links, graph.LinkEdit{Type: alm + "@" + l[1], ToKey: l[2]})
	}
	_, err = g.Commit(ctx, graph.Commit{Namespace: alm, Title: "Import demo data", Intent: "Seed demo data", By: "devseed", BaselineName: "Initial baseline", Edits: edits})
	return err == nil, err
}

func linkPartOf(e graph.NodeEdit, to domain.NodeRef) graph.NodeEdit {
	e.Links = append(e.Links, graph.LinkEdit{Type: access.LinkPartOf, To: &to})
	return e
}
