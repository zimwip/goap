package domain

import (
	"encoding/json"
	"maps"
	"slices"
)

// Drafts and versions at landing (ADR 0079). During a change a node has no version: its working state in the change is a
// Draft, one per change impact and flow, the fold (ADR 0029, 0030) of the impact events that carry it (not stored): `created`
// and `checkedOut` carry the initial state, `updated` and `transitioned` patches, `cancelled`, `withdrawn` and `landed`
// drop it. Landing (CommitChange) writes the node version from the draft and the draft goes.

// IsDraft reports a reference to the draft of a node in a change: the node, no version (Version 0). Outside a change a
// reference with Version 0 means the latest version of the node on main.
func (r NodeRef) IsDraft() bool { return r.ID != "" && r.Version == 0 }

// DraftRef is the reference to the draft of a node.
func DraftRef(id NodeID) NodeRef { return NodeRef{ID: id} }

// DraftLink is an outgoing link of a draft: To is an exact node version, or a draft reference (Version 0) to a node the
// change writes, resolved to the version landing writes for it.
type DraftLink struct {
	ID         LinkID         `json:"id"`
	Type       string         `json:"type"`
	To         NodeRef        `json:"to"`
	Properties map[string]any `json:"props,omitempty"`
}

// Draft is the working state of a node in a change on a flow: everything its next version will carry.
type Draft struct {
	Change ChangeID       `json:"changeId"`
	Impact ChangeImpactID `json:"impactId"`
	// Flow is the flow the draft belongs to ("" = the main flow): a flow checks the node out to write its own draft.
	Flow string `json:"flow,omitempty"`
	// Node is the id of the node, allocated when the draft is created and stable until landing.
	Node  NodeID `json:"node"`
	Key   string `json:"key"`
	Type  string `json:"type"`
	State string `json:"state,omitempty"`
	// Owner is the unit owning the next version; empty: carried over from its base, else the unit holding the change.
	Owner      NodeID         `json:"owner,omitempty"`
	Properties map[string]any `json:"props,omitempty"`
	// Origins are the nodes the node derives from (merge and split, ADR 0077), set on a creation.
	Origins []NodeRef `json:"origins,omitempty"`
	// Base is the version the draft was checked out from (nil for a creation).
	Base *NodeRef `json:"base,omitempty"`
	// Execution is the action run that checked the node out (what a relaunch of a step marks stale, ADR 0025).
	Execution string      `json:"execution,omitempty"`
	Links     []DraftLink `json:"links,omitempty"`
}

// Ref is the draft reference of the node.
func (d Draft) Ref() NodeRef { return DraftRef(d.Node) }

// Clone is a deep copy: a draft read from the cache is edited without touching it.
func (d Draft) Clone() Draft {
	d.Properties = cloneProps(d.Properties)
	d.Origins = slices.Clone(d.Origins)
	if d.Base != nil {
		b := *d.Base
		d.Base = &b
	}
	d.Links = slices.Clone(d.Links)
	for i := range d.Links {
		d.Links[i].Properties = cloneProps(d.Links[i].Properties)
	}
	return d
}

func cloneProps(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return maps.Clone(m)
}

// AsNode is the draft seen as a node (Version 0): what a reader of the change sees of the node. Branch and Namespace are
// set by the graph.
func (d Draft) AsNode() Node {
	return Node{ID: d.Node, Key: d.Key, Type: d.Type, State: d.State, Owner: d.Owner, Properties: cloneProps(d.Properties), Origins: slices.Clone(d.Origins),
		ChangeID: d.Change, ChangeImpact: d.Impact, Execution: d.Execution, Reason: draftReason(d)}
}

func draftReason(d Draft) string {
	if d.Base == nil {
		return ReasonCreate
	}
	return ReasonRevise
}

// OutLinks are the outgoing links of the draft as links of a node view (From is the draft reference).
func (d Draft) OutLinks() []Link {
	out := make([]Link, 0, len(d.Links))
	for _, l := range d.Links {
		out = append(out, Link{ID: l.ID, Type: l.Type, From: d.Ref(), To: l.To, Properties: l.Properties, ChangeID: d.Change})
	}
	return out
}

// Link returns the outgoing link with an id.
func (d Draft) Link(id LinkID) (DraftLink, bool) {
	for _, l := range d.Links {
		if l.ID == id {
			return l, true
		}
	}
	return DraftLink{}, false
}

// draftPatch is the typed form of ImpactEvent.Patch for what changes a draft. The event keeps the free-form map (the
// audit trail and PROV-O read it); the fold reads it through this shape, whatever the JSON decoding made of the numbers.
type draftPatch struct {
	Props   map[string]any `json:"props"`
	Unset   []string       `json:"unset"`
	OwnerID NodeID         `json:"ownerId"`
	State   *struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"state"`
	AddLink *struct {
		ID        LinkID         `json:"id"`
		Type      string         `json:"type"`
		ToID      NodeID         `json:"toId"`
		ToVersion Version        `json:"toVersion"`
		Props     map[string]any `json:"props"`
	} `json:"addLink"`
	UpdateLink *struct {
		ID    LinkID         `json:"id"`
		Props map[string]any `json:"props"`
	} `json:"updateLink"`
	RemoveLink *struct {
		ID LinkID `json:"id"`
	} `json:"removeLink"`
}

func patchOf(p map[string]any) (out draftPatch) {
	if len(p) == 0 {
		return out
	}
	b, err := json.Marshal(p)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

// DraftRow is the draft an event leaves for (impact, flow): the fold of one row.
func applyDraft(d *Draft, e ImpactEvent) *Draft {
	switch e.Op {
	case ImpactCreated, ImpactCheckedOut, ImpactTransitioned:
		if e.Draft != nil {
			n := e.Draft.Clone()
			n.Change, n.Impact, n.Flow = e.Change, e.Impact, e.Flow
			return &n
		}
	}
	if d == nil {
		return nil
	}
	switch e.Op {
	case ImpactUpdated, ImpactTransitioned:
		n := d.Clone()
		p := patchOf(e.Patch)
		if len(p.Props) > 0 || len(p.Unset) > 0 {
			if n.Properties == nil {
				n.Properties = map[string]any{}
			}
			maps.Copy(n.Properties, p.Props)
			for _, k := range p.Unset {
				delete(n.Properties, k)
			}
		}
		if p.OwnerID != "" {
			n.Owner = p.OwnerID
		}
		if p.State != nil {
			n.State = p.State.To
		}
		if a := p.AddLink; a != nil {
			n.Links = append(n.Links, DraftLink{ID: a.ID, Type: a.Type, To: NodeRef{ID: a.ToID, Version: a.ToVersion}, Properties: a.Props})
		}
		if u := p.UpdateLink; u != nil {
			for i := range n.Links {
				if n.Links[i].ID == u.ID {
					n.Links[i].Properties = u.Props
				}
			}
		}
		if r := p.RemoveLink; r != nil {
			n.Links = slices.DeleteFunc(n.Links, func(l DraftLink) bool { return l.ID == r.ID })
		}
		return &n
	case ImpactCancelled:
		return nil
	}
	return d
}

// ApplyDraftEvent returns the drafts of a change after an event; impacts are the change impacts after it (a draft whose
// impact is gone, or superseded by an adopted flow, goes with it). The list is not modified.
func ApplyDraftEvent(drafts []Draft, e ImpactEvent, impacts []ChangeImpact) []Draft {
	out := slices.Clone(drafts)
	at := slices.IndexFunc(out, func(d Draft) bool { return d.Impact == e.Impact && d.Flow == e.Flow })
	switch e.Op {
	case ImpactCreated, ImpactCheckedOut, ImpactTransitioned, ImpactUpdated, ImpactCancelled:
		var cur *Draft
		if at >= 0 {
			c := out[at]
			cur = &c
		}
		next := applyDraft(cur, e)
		switch {
		case next == nil && at >= 0:
			out = slices.Delete(out, at, at+1)
		case next != nil && at >= 0:
			out[at] = *next
		case next != nil:
			out = append(out, *next)
		}
	case ImpactWithdrawn, ImpactLanded:
		out = slices.DeleteFunc(out, func(d Draft) bool { return d.Impact == e.Impact })
	case ImpactAdopted:
		// the adopted flow's drafts were installed on the main flow by the events that follow (see the adoption)
		out = slices.DeleteFunc(out, func(d Draft) bool { return d.Flow == e.Flow })
	}
	return slices.DeleteFunc(out, func(d Draft) bool {
		i := slices.IndexFunc(impacts, func(c ChangeImpact) bool { return c.ID == d.Impact })
		return i < 0 || impacts[i].Superseded
	})
}

// FoldDrafts replays a log: the drafts of the change after the last event, in the order of the rows.
func FoldDrafts(events []ImpactEvent) []Draft {
	var drafts []Draft
	var impacts []ChangeImpact
	for _, e := range events {
		impacts = ApplyImpactEvent(impacts, e)
		drafts = ApplyDraftEvent(drafts, e, impacts)
	}
	return drafts
}

// DraftSeenBy is the draft a process on a flow sees for a change impact (ADR 0025 §3): the one of the innermost flow of
// the chain (the flow, then its ancestors, then the main flow) that holds one, as that flow's own events left it, not
// counting the events of the runs that are stale on the chain. Nil: none.
func DraftSeenBy(events []ImpactEvent, impact ChangeImpactID, chain []string, stale func(string) bool) *Draft {
	order := append(slices.Clone(chain), "")
	rows := map[string]*Draft{}
	for _, e := range events {
		if e.Impact != impact || !slices.Contains(order, e.Flow) || (stale(e.Execution) && e.Op != ImpactLanded && e.Op != ImpactWithdrawn) {
			continue
		}
		switch e.Op {
		case ImpactWithdrawn, ImpactLanded:
			clear(rows)
		case ImpactCreated, ImpactCheckedOut, ImpactTransitioned, ImpactUpdated, ImpactCancelled:
			rows[e.Flow] = applyDraft(rows[e.Flow], e)
		}
	}
	for _, f := range order {
		if d := rows[f]; d != nil {
			c := d.Clone()
			return &c
		}
	}
	return nil
}
