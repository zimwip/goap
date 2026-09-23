package engine

import (
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/metamodel"
)

// ItemInput is the external (LLM / human) representation of a change item.
// Domain nodes are designated by key (or "id@version"), items of the same
// batch by "#<ref>" and existing items by "@<itemId>".
type ItemInput struct {
	Ref         string         `json:"ref,omitempty"`
	Kind        string         `json:"kind"`
	Type        string         `json:"type,omitempty"`
	Target      string         `json:"target,omitempty"`
	Proposal    *ProposalInput `json:"proposal,omitempty"`
	Decision    *DecisionInput `json:"decision,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
	DerivedFrom []string       `json:"derivedFrom,omitempty"`
}

// ProposalInput is the external representation of a proposal.
type ProposalInput struct {
	Op   string `json:"op"`
	Node *struct {
		Base  string         `json:"base,omitempty"`
		Key   string         `json:"key,omitempty"`
		Type  string         `json:"type,omitempty"`
		Props map[string]any `json:"props,omitempty"`
	} `json:"node,omitempty"`
	Link *struct {
		ID    string         `json:"id,omitempty"`
		Type  string         `json:"type,omitempty"`
		From  string         `json:"from,omitempty"`
		To    string         `json:"to,omitempty"`
		Props map[string]any `json:"props,omitempty"`
	} `json:"link,omitempty"`
}

// DecisionInput is the external representation of a decision.
type DecisionInput struct {
	Item    string `json:"item"`
	Accept  bool   `json:"accept"`
	Comment string `json:"comment,omitempty"`
}

// resolver turns ItemInputs into domain items against the reference baseline.
type resolver struct {
	byKey  map[string]domain.NodeRef
	byID   map[domain.NodeID]domain.NodeRef
	byType map[string]domain.NodeRef // NodeType name → node (metadata layer, ADR 0012)
	items  map[domain.ItemID]bool
	local  map[string]domain.ItemID
	newID  func() string
}

func newResolver(nodes []domain.Node, change domain.ChangeSet, newID func() string) *resolver {
	r := &resolver{byKey: map[string]domain.NodeRef{}, byID: map[domain.NodeID]domain.NodeRef{}, byType: map[string]domain.NodeRef{},
		items: map[domain.ItemID]bool{}, local: map[string]domain.ItemID{}, newID: newID}
	ns := metamodel.TypeNamespace(nodes, change.Methodology)
	for _, n := range nodes {
		r.byKey[n.Key] = n.Ref()
		r.byID[n.ID] = n.Ref()
		if name, ok := metamodel.TypeName(n.Key, ns); ok {
			r.byType[name] = n.Ref()
		}
	}
	for _, it := range change.Items {
		r.items[it.ID] = true
	}
	return r
}

func (r *resolver) node(s string) (domain.NodeRef, error) {
	s = strings.TrimSpace(s)
	if ref, ok := r.byKey[s]; ok {
		return ref, nil
	}
	id := s
	if i := strings.LastIndex(s, "@"); i > 0 {
		id = s[:i]
	}
	if ref, ok := r.byID[domain.NodeID(id)]; ok {
		return ref, nil
	}
	return domain.NodeRef{}, fmt.Errorf("unknown node %q in reference baseline", s)
}

func (r *resolver) item(s string) (domain.ItemID, error) {
	switch {
	case strings.HasPrefix(s, "#"):
		if id, ok := r.local[s[1:]]; ok {
			return id, nil
		}
		return "", fmt.Errorf("unknown local item %q", s)
	case strings.HasPrefix(s, "@"):
		if id := domain.ItemID(s[1:]); r.items[id] {
			return id, nil
		}
		return "", fmt.Errorf("unknown item %q", s)
	}
	if r.items[domain.ItemID(s)] {
		return domain.ItemID(s), nil
	}
	if id, ok := r.local[s]; ok {
		return id, nil
	}
	return "", fmt.Errorf("unknown item %q", s)
}

func (r *resolver) endpoint(s string) (domain.Endpoint, error) {
	if strings.HasPrefix(s, "#") || strings.HasPrefix(s, "@") {
		id, err := r.item(s)
		return domain.Endpoint{Item: id}, err
	}
	ref, err := r.node(s)
	return domain.Endpoint{Node: &ref}, err
}

// resolve converts a batch. Item ids are assigned here so that "#ref"
// references inside the batch can be resolved.
func (r *resolver) resolve(in []ItemInput, producedBy string) ([]domain.ChangeItem, error) {
	for i, it := range in {
		id := domain.ItemID(r.newID())
		if it.Ref != "" {
			r.local[it.Ref] = id
		}
		r.local[fmt.Sprintf("%d", i)] = id
	}
	out := make([]domain.ChangeItem, 0, len(in))
	for i, it := range in {
		item := domain.ChangeItem{ID: r.local[fmt.Sprintf("%d", i)], Kind: domain.ItemKind(it.Kind), Type: it.Type, Data: it.Data, ProducedBy: producedBy}
		for _, d := range it.DerivedFrom {
			id, err := r.item(d)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			item.DerivedFrom = append(item.DerivedFrom, id)
		}
		if it.Target != "" {
			ref, err := r.node(it.Target)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			item.Target = &ref
		}
		var instanceOf *domain.ChangeItem // companion instanceOf link, appended after item below
		if p := it.Proposal; p != nil {
			dp := &domain.Proposal{Op: domain.ProposalOp(p.Op)}
			if p.Node != nil {
				dp.Node = &domain.NodeDraft{Key: p.Node.Key, Type: p.Node.Type, Properties: p.Node.Props}
				if p.Node.Base != "" {
					ref, err := r.node(p.Node.Base)
					if err != nil {
						return nil, fmt.Errorf("item %d: %w", i, err)
					}
					dp.Node.Base = &ref
				}
				if dp.Op == domain.OpCreateNode {
					if typeRef, ok := r.byType[p.Node.Type]; ok {
						instanceOf = &domain.ChangeItem{ID: domain.ItemID(r.newID()), Kind: domain.KindProposal, Type: "metamodel", ProducedBy: producedBy,
							DerivedFrom: []domain.ItemID{item.ID},
							Proposal: &domain.Proposal{Op: domain.OpAddLink, Link: &domain.LinkDraft{
								Type: metamodel.LinkInstanceOf, From: domain.Endpoint{Item: item.ID}, To: domain.Endpoint{Node: &typeRef}}}}
					}
				}
			}
			if p.Link != nil {
				dp.Link = &domain.LinkDraft{LinkID: domain.LinkID(p.Link.ID), Type: p.Link.Type, Properties: p.Link.Props}
				if p.Link.From != "" {
					e, err := r.endpoint(p.Link.From)
					if err != nil {
						return nil, fmt.Errorf("item %d: %w", i, err)
					}
					dp.Link.From = e
				}
				if p.Link.To != "" {
					e, err := r.endpoint(p.Link.To)
					if err != nil {
						return nil, fmt.Errorf("item %d: %w", i, err)
					}
					dp.Link.To = e
				}
			}
			item.Proposal = dp
		}
		if d := it.Decision; d != nil {
			id, err := r.item(d.Item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			item.Decision = &domain.Decision{Item: id, Accept: d.Accept, Comment: d.Comment}
		}
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		out = append(out, item)
		if instanceOf != nil {
			out = append(out, *instanceOf)
		}
	}
	return out, nil
}
