package engine

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/metamodel"
)

func newID() func() string {
	n := 0
	return func() string { n++; return string(rune('a' + n)) }
}

func TestResolverAutoLinksInstanceOf(t *testing.T) {
	ntRef := domain.NodeRef{ID: "nt-req", Version: 1}
	nodes := []domain.Node{
		{ID: ntRef.ID, Version: ntRef.Version, Key: metamodel.Key("sdlc", metamodel.TypeNodeType, "Requirement"), Type: metamodel.TypeNodeType},
	}
	change := domain.ChangeSet{Methodology: "sdlc"}
	r := newResolver(nodes, change, newID())
	items, err := r.resolve([]ItemInput{{
		Kind: "proposal",
		Proposal: &ProposalInput{Op: "create_node", Node: &struct {
			Base  string         `json:"base,omitempty"`
			Key   string         `json:"key,omitempty"`
			Type  string         `json:"type,omitempty"`
			Props map[string]any `json:"props,omitempty"`
			State string         `json:"state,omitempty"`
		}{Key: "REQ-10", Type: "Requirement"}},
	}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected the create_node item plus a companion instanceOf link, got %d: %+v", len(items), items)
	}
	create, link := items[0], items[1]
	if create.Proposal.Op != domain.OpCreateNode {
		t.Fatalf("items[0] = %+v, want create_node", create)
	}
	if link.Proposal.Op != domain.OpAddLink || link.Proposal.Link.Type != metamodel.LinkInstanceOf {
		t.Fatalf("items[1] = %+v, want add_link instanceOf", link)
	}
	if link.Proposal.Link.From.Item != create.ID {
		t.Fatalf("instanceOf link.From = %v, want the created item %v", link.Proposal.Link.From, create.ID)
	}
	if link.Proposal.Link.To.Node == nil || *link.Proposal.Link.To.Node != ntRef {
		t.Fatalf("instanceOf link.To = %+v, want %v", link.Proposal.Link.To, ntRef)
	}
}

func TestResolverSkipsUnknownType(t *testing.T) {
	change := domain.ChangeSet{Methodology: "sdlc"}
	r := newResolver(nil, change, newID())
	items, err := r.resolve([]ItemInput{{
		Kind: "proposal",
		Proposal: &ProposalInput{Op: "create_node", Node: &struct {
			Base  string         `json:"base,omitempty"`
			Key   string         `json:"key,omitempty"`
			Type  string         `json:"type,omitempty"`
			Props map[string]any `json:"props,omitempty"`
			State string         `json:"state,omitempty"`
		}{Key: "REQ-10", Type: "Requirement"}},
	}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("no known NodeType: expected only the create_node item, got %d: %+v", len(items), items)
	}
}
