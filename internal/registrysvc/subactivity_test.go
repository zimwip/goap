package registrysvc

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// A sub-change's Activity, when it names one, must be the parent's own or a descendant of it reached by
// sub_activity links (architecture plan "Activity concept" cascade); unset is fine (no inheritance, since a
// sub-change is usually scoped to a more specific sub-activity, not the parent's own).
func TestSubChangeActivityCascade(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	store := graphWithDomains{NewGraphStore(g), NewMemoryStore()}
	reg := &Service{Store: store}
	g.SubChangeValidator = reg.SubChangeValidator

	mk := func(key string, links ...graph.LinkEdit) graph.NodeEdit {
		return graph.NodeEdit{Key: key, Type: "methodology@Process", Links: links}
	}
	head, err := g.BranchHead(ctx, NamespaceMethodology, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit(ctx, graph.Commit{Namespace: NamespaceMethodology, Title: "activities", Baseline: head.ID, By: "test", Edits: []graph.NodeEdit{
		mk("ACT-PARENT", graph.LinkEdit{Type: linkSubActivity, ToKey: "ACT-CHILD"}),
		mk("ACT-CHILD"),
		mk("ACT-OTHER"),
	}}); err != nil {
		t.Fatal(err)
	}

	scoped := func(ref string) map[string]any { return map[string]any{DataActivity: ref} }
	base, err := g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := g.CreateChange(ctx, graph.NewChange{Title: "p", BaselineID: base.ID, OwnBranch: true, Data: scoped("ACT-PARENT")})
	if err != nil {
		t.Fatal(err)
	}
	within, err := g.CreateChange(ctx, graph.NewChange{Title: "within", ParentID: parent.ID, Data: scoped("ACT-CHILD")})
	if err != nil {
		t.Fatalf("activity within the parent's: %v", err)
	}
	if ActivityOf(within) != "ACT-CHILD" {
		t.Fatalf("activity = %q", ActivityOf(within))
	}
	if _, err := g.CreateChange(ctx, graph.NewChange{Title: "outside", ParentID: parent.ID, Data: scoped("ACT-OTHER")}); !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("activity outside the parent's: %v", err)
	}
	if noAct, err := g.CreateChange(ctx, graph.NewChange{Title: "unscoped", ParentID: parent.ID}); err != nil || ActivityOf(noAct) != "" {
		t.Fatalf("activity not inherited: %+v, %v", noAct, err)
	}
}
