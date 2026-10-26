package domain

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"

	"github.com/zimwip/goap/contracts/lnk"
)

// Drafts and versions at commit (ADR 0079). During a change a node has no version: its working state in the change is a
// Draft, one per change impact and flow, the fold (ADR 0029, 0030) of the impact events that carry it (not stored): `created`
// and `checkedOut` carry the initial state, `updated` and `transitioned` patches, `cancelled`, `withdrawn` and `committed`
// drop it. Commit (CommitChange) writes the node version from the draft and the draft goes.

// IsDraft reports a reference to the draft of a node in a change: the node, no version (Version 0). Outside a change a
// reference with Version 0 means the latest version of the node on main.
func (r NodeRef) IsDraft() bool { return r.ID != "" && r.Version == 0 }

// DraftRef is the reference to the draft of a node.
func DraftRef(id NodeID) NodeRef { return NodeRef{ID: id} }

// DraftLink is an outgoing link of a draft: To is an exact node version, or a draft reference (Version 0) to a node the
// change writes, resolved to the version commit writes for it.
type DraftLink struct {
	ID   LinkID  `json:"id"`
	Type string  `json:"type"`
	To   NodeRef `json:"to"`
	// Target is the URL of a target in another source (Link.Target), with To zero.
	Target     lnk.URL        `json:"target,omitzero"`
	Properties map[string]any `json:"props,omitempty"`
}

// External reports a link to another source.
func (l DraftLink) External() bool { return l.To.IsZero() && !l.Target.IsZero() }

// Draft is the working state of a node in a change on a flow: everything its next version will carry.
type Draft struct {
	Change ChangeID       `json:"changeId"`
	Impact ChangeImpactID `json:"impactId"`
	// Flow is the flow the draft belongs to ("" = the main flow): a flow checks the node out to write its own draft.
	Flow string `json:"flow,omitempty"`
	// Node is the id of the node, allocated when the draft is created and stable until commit.
	Node  NodeID `json:"node"`
	Key   string `json:"key"`
	Type  string `json:"type"`
	State string `json:"state,omitempty"`
	// Owner is the unit owning the next version; empty: carried over from its base, else the unit holding the change.
	Owner      lnk.URL        `json:"owner,omitzero"`
	Properties map[string]any `json:"props,omitempty"`
	// Origins are the nodes the node derives from (merge and split, ADR 0077), set on a creation.
	Origins []NodeRef `json:"origins,omitempty"`
	// Base is the version the draft was checked out from (nil for a creation).
	Base *NodeRef `json:"base,omitempty"`
	// MergedWith is, for the draft a rebase of a committed change merged (ADR 0102), the version on the head of the target
	// branch it was merged with: the second parent of the version it commits as.
	MergedWith *NodeRef `json:"mergedWith,omitempty"`
	// Execution is the action run that checked the node out (what a relaunch of a step marks stale, ADR 0025).
	Execution string      `json:"execution,omitempty"`
	Links     []DraftLink `json:"links,omitempty"`
	// Creator is, on a creation, the person who created the draft (ADR 0161 §3: the author of its created event, the end
	// user when an agent created it for them), the creator of the node it commits.
	Creator string `json:"creator,omitempty"`
	// Editor is the person who last edited the draft (the author of its last event): the editor of the version it
	// commits as. Not stored: rebuilt from the log.
	Editor string `json:"-"`
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
	if d.MergedWith != nil {
		m := *d.MergedWith
		d.MergedWith = &m
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
	if d.MergedWith != nil && d.MergedWith.Version != d.Base.Version {
		return ReasonRebase
	}
	return ReasonRevise
}

// OutLinks are the outgoing links of the draft as links of a node view (From is the draft reference).
func (d Draft) OutLinks() []Link {
	out := make([]Link, 0, len(d.Links))
	for _, l := range d.Links {
		out = append(out, Link{ID: l.ID, Type: l.Type, From: d.Ref(), To: l.To, Target: l.Target, Properties: l.Properties, ChangeID: d.Change})
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
	Props map[string]any `json:"props"`
	Unset []string       `json:"unset"`
	Owner lnk.URL        `json:"owner"`
	State *struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"state"`
	AddLink    *patchLink `json:"addLink"`
	UpdateLink *patchLink `json:"updateLink"`
	RemoveLink *patchLink `json:"removeLink"`
	// the links a patch of several changes (DraftDiff: the merge of a rebase, ADR 0144) adds, updates and removes
	AddLinks    []patchLink `json:"addLinks"`
	UpdateLinks []patchLink `json:"updateLinks"`
	RemoveLinks []patchLink `json:"removeLinks"`
}

// patchLink is a link a patch adds (id, type, target, properties), updates (id, properties) or removes (id).
type patchLink struct {
	ID        LinkID         `json:"id"`
	Type      string         `json:"type,omitempty"`
	ToID      NodeID         `json:"toId,omitempty"`
	ToVersion Version        `json:"toVersion,omitempty"`
	Target    string         `json:"target,omitempty"`
	Props     map[string]any `json:"props,omitempty"`
}

// linksOf is the link of a patch of one change and the links of a patch of several.
func linksOf(one *patchLink, many []patchLink) []patchLink {
	if one == nil {
		return many
	}
	return append([]patchLink{*one}, many...)
}

// DraftDiff is the patch of an updated event that turns a draft into another (props, unset, owner, state, and the
// links as addLinks / updateLinks / removeLinks): the difference a rebase's merge makes to the version the change
// committed (ADR 0144), what a reader shows. A link of to that from holds alike (type, target, properties) keeps the id
// it has in from; nil when they hold the same.
func DraftDiff(from, to Draft) map[string]any {
	p := map[string]any{}
	props := map[string]any{}
	for k, v := range to.Properties {
		if w, ok := from.Properties[k]; !ok || !reflect.DeepEqual(v, w) {
			props[k] = v
		}
	}
	if len(props) > 0 {
		p["props"] = props
	}
	var unset []string
	for k := range from.Properties {
		if _, ok := to.Properties[k]; !ok {
			unset = append(unset, k)
		}
	}
	if len(unset) > 0 {
		slices.Sort(unset)
		p["unset"] = unset
	}
	if to.Owner != from.Owner {
		p["owner"] = to.Owner.String()
	}
	if to.State != from.State {
		p["state"] = map[string]any{"from": from.State, "to": to.State}
	}
	var adds, updates, removes []any
	used := make([]bool, len(to.Links))
	match := func(l DraftLink, sameProps bool) int {
		for i, m := range to.Links {
			if !used[i] && m.Type == l.Type && m.To == l.To && m.Target == l.Target && (!sameProps || reflect.DeepEqual(m.Properties, l.Properties)) {
				return i
			}
		}
		return -1
	}
	var left []DraftLink
	for _, l := range from.Links {
		if i := match(l, true); i >= 0 {
			used[i] = true
		} else {
			left = append(left, l)
		}
	}
	for _, l := range left {
		if i := match(l, false); i >= 0 {
			used[i] = true
			updates = append(updates, map[string]any{"id": l.ID, "props": to.Links[i].Properties})
		} else {
			removes = append(removes, map[string]any{"id": l.ID})
		}
	}
	for i, l := range to.Links {
		if !used[i] {
			add := map[string]any{"id": l.ID, "type": l.Type, "toId": l.To.ID, "toVersion": l.To.Version, "props": l.Properties}
			if l.External() {
				add = map[string]any{"id": l.ID, "type": l.Type, "target": l.Target.String(), "props": l.Properties}
			}
			adds = append(adds, add)
		}
	}
	for k, v := range map[string][]any{"addLinks": adds, "updateLinks": updates, "removeLinks": removes} {
		if len(v) > 0 {
			p[k] = v
		}
	}
	if len(p) == 0 {
		return nil
	}
	return p
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

// ApplyDraftTo is the draft an event of its change impact and flow leaves of a draft (nil: none, or no more); d is not
// modified.
func ApplyDraftTo(d *Draft, e ImpactEvent) *Draft { return applyDraft(d, e) }

// DraftRow is the draft an event leaves for (impact, flow): the fold of one row.
func applyDraft(d *Draft, e ImpactEvent) *Draft {
	switch e.Op {
	case ImpactCreated, ImpactCheckedOut, ImpactTransitioned:
		if e.Draft != nil {
			n := e.Draft.Clone()
			n.Change, n.Impact, n.Flow = e.Change, e.Impact, e.Flow
			if e.Op == ImpactCreated && n.Creator == "" {
				n.Creator = e.Author()
			}
			if a := e.Author(); a != "" {
				n.Editor = a
			}
			return &n
		}
	}
	if d == nil {
		return nil
	}
	switch e.Op {
	case ImpactUpdated, ImpactTransitioned:
		n := d.Clone()
		if a := e.Author(); a != "" {
			n.Editor = a
		}
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
		if !p.Owner.IsZero() {
			n.Owner = p.Owner
		}
		if p.State != nil {
			n.State = p.State.To
		}
		for _, a := range linksOf(p.AddLink, p.AddLinks) {
			l := DraftLink{ID: a.ID, Type: a.Type, To: NodeRef{ID: a.ToID, Version: a.ToVersion}, Properties: a.Props}
			if a.ToID == "" && a.Target != "" {
				l.Target, _ = lnk.Parse(a.Target)
			}
			n.Links = append(n.Links, l)
		}
		for _, u := range linksOf(p.UpdateLink, p.UpdateLinks) {
			for i := range n.Links {
				if n.Links[i].ID == u.ID {
					n.Links[i].Properties = u.Props
				}
			}
		}
		for _, r := range linksOf(p.RemoveLink, p.RemoveLinks) {
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
	case ImpactWithdrawn, ImpactCommitted:
		out = slices.DeleteFunc(out, func(d Draft) bool { return d.Impact == e.Impact })
	case ImpactAdopted:
		// the adopted flow's drafts were installed on the main flow by the events that follow (see the adoption)
		out = slices.DeleteFunc(out, func(d Draft) bool { return d.Flow == e.Flow })
	}
	live := make(map[ChangeImpactID]bool, len(impacts))
	for _, c := range impacts {
		live[c.ID] = !c.Superseded
	}
	return slices.DeleteFunc(out, func(d Draft) bool { return !live[d.Impact] })
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
		gone := e.Op == ImpactCommitted || e.Op == ImpactWithdrawn
		if e.Impact != impact || !slices.Contains(order, e.Flow) || (stale(e.Execution) && !gone) {
			continue
		}
		switch e.Op {
		case ImpactWithdrawn, ImpactCommitted:
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
