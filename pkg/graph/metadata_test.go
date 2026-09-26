package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// metaWorld: Task and Story extend Item, Person is unrelated; "assigned" joins a Task to a Person; the graph declares
// the namespaces "sdlc" and "platform" only.
func metaWorld(t *testing.T) (*Graph, domain.Baseline, map[string]domain.Node) {
	t.Helper()
	ctx := context.Background()
	g := New(NewMemory())
	nodes := map[string]domain.Node{}
	var refs []domain.NodeRef
	mk := func(key, typ string, props map[string]any) {
		n, err := g.CreateNode(ctx, NewNode{Key: key, Type: typ, Properties: props})
		if err != nil {
			t.Fatal(err)
		}
		nodes[key] = n
		refs = append(refs, n.Ref())
	}
	mk("T:Item", NodeTypeNode, map[string]any{"name": "Item"})
	mk("T:Task", NodeTypeNode, map[string]any{"name": "Task", "extends": "Item"})
	mk("T:Story", NodeTypeNode, map[string]any{"name": "Story", "extends": "Item"})
	mk("T:Person", NodeTypeNode, map[string]any{"name": "Person"})
	mk("L:assigned", LinkTypeNode, map[string]any{"name": "assigned", "from": "Task", "to": "Person"})
	mk("L:relates", LinkTypeNode, map[string]any{"name": "relates", "from": "Item"})
	mk("NS:sdlc", NamespaceNode, map[string]any{"name": "sdlc"})
	mk("NS:platform", NamespaceNode, map[string]any{"name": "platform"})
	mk("TASK-1", "Task", nil)
	mk("STORY-1", "Story", nil)
	mk("ANN", "Person", nil)
	mk("THING", "Unmanaged", nil)
	b, err := g.CreateBaseline(ctx, "meta", refs)
	if err != nil {
		t.Fatal(err)
	}
	return g, b, nodes
}

func addLink(t *testing.T, g *Graph, b domain.Baseline, typ string, from, to domain.Node) error {
	t.Helper()
	ctx := context.Background()
	c, err := g.CreateChange(ctx, NewChange{Title: "l", BaselineID: b.ID, Namespace: "sdlc"})
	if err != nil {
		t.Fatal(err)
	}
	fr, tr := from.Ref(), to.Ref()
	_, err = g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpAddLink,
		Link: &domain.LinkDraft{Type: typ, From: domain.Endpoint{Node: &fr}, To: domain.Endpoint{Node: &tr}}}}})
	return err
}

func TestLinkTypesRestrictWhatLinksJoin(t *testing.T) {
	g, b, n := metaWorld(t)
	if err := addLink(t, g, b, "assigned", n["TASK-1"], n["ANN"]); err != nil {
		t.Fatalf("declared pair: %v", err)
	}
	if err := addLink(t, g, b, "assigned", n["STORY-1"], n["ANN"]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a Story is not a Task: %v", err)
	}
	if err := addLink(t, g, b, "assigned", n["TASK-1"], n["STORY-1"]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("to must be a Person: %v", err)
	}
	// a subtype may stand where its supertype is declared; an unset end accepts anything
	if err := addLink(t, g, b, "relates", n["STORY-1"], n["ANN"]); err != nil {
		t.Fatalf("subtype: %v", err)
	}
	// nothing is known of a node whose type is not on the graph, nor of a link type nobody declares
	if err := addLink(t, g, b, "assigned", n["TASK-1"], n["THING"]); err != nil {
		t.Fatalf("unmanaged end: %v", err)
	}
	if err := addLink(t, g, b, "mentions", n["ANN"], n["STORY-1"]); err != nil {
		t.Fatalf("undeclared link type: %v", err)
	}
}

func TestChangesActOnDeclaredNamespaces(t *testing.T) {
	g, b, _ := metaWorld(t)
	ctx := context.Background()
	if _, err := g.CreateChange(ctx, NewChange{Title: "ok", BaselineID: b.ID, Namespace: "platform"}); err != nil {
		t.Fatalf("declared: %v", err)
	}
	if _, err := g.CreateChange(ctx, NewChange{Title: "no", BaselineID: b.ID, Namespace: "elsewhere"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("undeclared namespace: %v", err)
	}
}
