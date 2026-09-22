package graphsvc

import (
	"context"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// SeedDemo loads a small requirements repository when the graph is empty,
// matching methodologies/impact-analysis.yaml.
func SeedDemo(ctx context.Context, g *graph.Graph) (bool, error) {
	bs, err := g.Baselines(ctx)
	if err != nil || len(bs) > 0 {
		return false, err
	}
	nodes := []graph.NewNode{
		{Key: "NEED-1", Type: "Need", Properties: map[string]any{"title": "Payer ses commandes en ligne"}},
		{Key: "NEED-2", Type: "Need", Properties: map[string]any{"title": "Être remboursé rapidement"}},
		{Key: "REQ-1", Type: "Requirement", Properties: map[string]any{"title": "Le paiement carte passe par le PSP Acme (API v1)", "priority": "high"}},
		{Key: "REQ-2", Type: "Requirement", Properties: map[string]any{"title": "Le remboursement est initié sous 24h via le PSP", "priority": "medium"}},
		{Key: "REQ-3", Type: "Requirement", Properties: map[string]any{"title": "Les reçus sont envoyés par email", "priority": "low"}},
		{Key: "TST-1", Type: "TestCase", Properties: map[string]any{"title": "Paiement carte nominal"}},
		{Key: "TST-2", Type: "TestCase", Properties: map[string]any{"title": "Remboursement total"}},
		{Key: "TST-3", Type: "TestCase", Properties: map[string]any{"title": "Réception du reçu"}},
		{Key: "CMP-1", Type: "Component", Properties: map[string]any{"title": "payment-service", "owner": "team-checkout"}},
		{Key: "CMP-2", Type: "Component", Properties: map[string]any{"title": "notification-service", "owner": "team-crm"}},
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
	}
	for _, l := range links {
		if _, err := g.Link(ctx, l[1], refs[l[0]], refs[l[2]], nil); err != nil {
			return false, err
		}
	}
	_, err = g.CreateBaselineFromLatest(ctx, "Référentiel initial")
	return err == nil, err
}
