package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
)

// The payload of an impact event in the log (ADR 0029, 0030, 0142) holds what the event says, never the envelope the
// columns of the log entry already hold (id, change, impact, flow, execution, caller, time, causation), nor a node it
// copies as is: a checkout of a stored version records the version (BaseCopy), not its properties and links. A reader
// rebuilds the event with ImpactEventOf, the graph hydrates a copy from the version it names.

// BaseCopy marks a draft that is the copy of the version it was checked out from (Draft.Base): its key, type, state,
// owner, properties and links are those of the version, each link with the id BaseLinkID gives it. Drafted are the
// targets of those links the change holds a draft of: the draft copies a link to one as a link to the draft (Version 0).
type BaseCopy struct {
	Drafted []NodeRef `json:"drafted,omitempty"`
}

// baseLinkSpace is the namespace of the ids of the links of a copied draft.
var baseLinkSpace = uuid.MustParse("6f1c0f0e-8d1b-4b6a-9a52-2d6e3c1f7a40")

// BaseLinkID is the id the draft a checkout event copies gives the stored link: derived from both, so the draft the log
// rebuilds holds the ids the edits after it name.
func BaseLinkID(event string, link LinkID) LinkID {
	return LinkID(uuid.NewSHA1(baseLinkSpace, []byte(event+"\x00"+string(link))).String())
}

// CopyDraft is the draft BaseCopy describes: the version base and its outgoing links copied, the links to Drafted
// targets turned into links to the drafts. Change, impact, flow and execution are the event's.
func CopyDraft(e ImpactEvent, base Node, links []Link) Draft {
	b := base.Ref()
	d := Draft{Change: e.Change, Impact: e.Impact, Flow: e.Flow, Execution: e.Execution, Node: base.ID, Key: base.Key, Type: base.Type,
		State: base.State, Owner: base.Responsible, Properties: cloneProps(base.Properties), Base: &b}
	if e.Draft != nil {
		d.Execution = e.Draft.Execution
		if m := e.Draft.MergedWith; m != nil {
			mw := *m // the checkout of a rebase's merge (ADR 0144)
			d.MergedWith = &mw
		}
	}
	var drafted []NodeRef
	if e.FromBase != nil {
		drafted = e.FromBase.Drafted
	}
	for _, l := range links {
		to := l.To
		for _, r := range drafted {
			if r == to {
				to = DraftRef(to.ID)
				break
			}
		}
		d.Links = append(d.Links, DraftLink{ID: BaseLinkID(e.ID, l.ID), Type: l.Type, To: to, Target: l.Target, Properties: cloneProps(l.Properties)})
	}
	return d
}

// Hydrated reports whether the draft of a BaseCopy event has been rebuilt from its version (ImpactEventOf leaves only
// its base).
func (e ImpactEvent) Hydrated() bool {
	if e.Op == ImpactCommitted {
		return e.Committed != nil && e.Committed.Version > 0 && e.Impact != ""
	}
	return e.FromBase == nil || (e.Draft != nil && e.Draft.Key != "")
}

// absent shadows a field of an embedded value in a payload: a nil pointer, omitted.
type absent *struct{}

// impactPayload is an ImpactEvent as the log stores it: the envelope left to the columns.
type impactPayload struct {
	ImpactEvent
	ID        absent          `json:"id,omitempty"`
	Change    absent          `json:"changeId,omitempty"`
	Seq       absent          `json:"seq,omitempty"`
	Impact    absent          `json:"impactId,omitempty"`
	Flow      absent          `json:"flow,omitempty"`
	Execution absent          `json:"execution,omitempty"`
	By        absent          `json:"by,omitempty"`
	At        absent          `json:"at,omitempty"`
	Causation absent          `json:"causation,omitempty"`
	State     any             `json:"state,omitempty"` // *statePayload, else the change impact whole
	Draft     any             `json:"draft,omitempty"` // *draftPayload, *basePayload
	PostRaw   json.RawMessage `json:"post,omitempty"`  // the post, left out where it is implied (draftNode), null for none there
}

// statePayload is the change impact a proposed or created event adds, its id being the event's impact. On a created
// event its key and type are the draft's: left out when they are (nil), written otherwise, "" included.
type statePayload struct {
	ChangeImpact
	ID   absent  `json:"id,omitempty"`
	Key  *string `json:"key,omitempty"`
	Type *string `json:"type,omitempty"`
}

// draftPayload is a draft in a payload: its change, impact and flow are the event's (applyDraft sets them).
type draftPayload struct {
	Draft
	Change absent `json:"changeId,omitempty"`
	Impact absent `json:"impactId,omitempty"`
	Flow   absent `json:"flow,omitempty"`
}

// basePayload is the draft of a BaseCopy event: the version it copies says the rest (its node, key, type...).
type basePayload struct {
	Base       *NodeRef `json:"base,omitempty"`
	MergedWith *NodeRef `json:"mergedWith,omitempty"`
	Execution  string   `json:"execution,omitempty"`
}

// draftNode is the node of the draft an event installs, which its post is the draft reference of: the copied version's
// on a BaseCopy event, else the draft's; "" when the event installs none.
func (e ImpactEvent) draftNode() NodeID {
	switch {
	case e.Draft == nil || (e.Op != ImpactCreated && e.Op != ImpactCheckedOut):
		return ""
	case e.FromBase != nil:
		if e.Draft.Base == nil {
			return ""
		}
		return e.Draft.Base.ID
	}
	return e.Draft.Node
}

// nullPost is the post of an event that installs a draft and leaves the impact with no post (none today): written as
// null, since a post left out is the draft reference of its node.
var nullPost = json.RawMessage("null")

func marshalImpact(e ImpactEvent) ([]byte, error) {
	p := impactPayload{ImpactEvent: e}
	// the post of an event installing a draft is the draft reference of its node: left out
	switch n := e.draftNode(); {
	case e.Post == nil && n != "":
		p.PostRaw = nullPost
	case e.Post != nil && (n == "" || *e.Post != DraftRef(n)):
		raw, err := json.Marshal(e.Post)
		if err != nil {
			return nil, err
		}
		p.PostRaw = raw
	}
	if e.State != nil {
		if e.State.ID == e.Impact {
			sp := &statePayload{ChangeImpact: *e.State}
			key, typ := e.State.Key, e.State.Type
			sp.Key, sp.Type = &key, &typ
			if e.Op == ImpactCreated && e.Draft != nil && e.FromBase == nil {
				// a creation's key and type are its draft's
				if key == e.Draft.Key {
					sp.Key = nil
				}
				if typ == e.Draft.Type {
					sp.Type = nil
				}
			}
			p.State = sp
		} else {
			p.State = e.State // another impact's (none today): kept whole
		}
	}
	if d := e.Draft; d != nil {
		if e.FromBase != nil {
			// the copy of a version: the version says the rest
			p.Draft = &basePayload{Base: d.Base, Execution: d.Execution, MergedWith: d.MergedWith}
		} else {
			p.Draft = &draftPayload{Draft: *d}
		}
	}
	return json.Marshal(p)
}

// impactPayloadIn reads a payload: what marshalImpact leaves out told apart from what it writes empty.
type impactPayloadIn struct {
	ImpactEvent
	State *stateIn        `json:"state"`
	Post  json.RawMessage `json:"post"`
}

type stateIn struct {
	ChangeImpact
	Key  *string `json:"key"`
	Type *string `json:"type"`
}

// One log entry may carry several events of one operation (ADR 0144): the verdicts of a review, the versions of a
// commit. The payload of such an entry is {"impacts": [part...]} with the keys every part has alike hoisted next to it;
// a part is the payload of its event with its "impactId". The envelope (the columns) is the events', which share it.

// batchKey is the key of the parts of an entry carrying several events.
const batchKey = "impacts"

// Batched reports the operations whose events of one operation share a log entry (ADR 0144).
func (o ImpactOp) Batched() bool { return o == ImpactReviewed || o == ImpactCommitted }

// SameEntry reports events one log entry can carry: of one change and one batched operation, with one envelope.
func SameEntry(a, b ImpactEvent) bool {
	return a.Op.Batched() && a.Change == b.Change && a.Op == b.Op && a.Flow == b.Flow && a.Execution == b.Execution && a.By == b.By && a.For == b.For &&
		a.Causation == b.Causation
}

// PartID is the id of the n-th event an entry carries.
func PartID(entry string, n int) string { return entry + "#" + strconv.Itoa(n) }

// ImpactBatchEntry is the log entry carrying several events (SameEntry, in their order) under the id given; its time is
// the first event's. ImpactEventsOf reads them back, with the ids PartID gives. A part holding its impact only is
// written as the impact's id (the verdicts of one review); the events of a commit are one commit (CommitEntry).
func ImpactBatchEntry(id string, events []ImpactEvent) (LogEntry, error) {
	if len(events) == 0 {
		return LogEntry{}, fmt.Errorf("an entry carries at least one event")
	}
	first := events[0]
	if first.Op == ImpactCommitted {
		return CommitEntry(id, events)
	}
	parts := make([]map[string]json.RawMessage, len(events))
	for i, e := range events {
		if !SameEntry(first, e) {
			return LogEntry{}, fmt.Errorf("event %d (%s of %s) is not carried with the %s of %s", i, e.Op, e.Impact, first.Op, first.Impact)
		}
		raw, err := marshalImpact(e)
		if err != nil {
			return LogEntry{}, err
		}
		if err := json.Unmarshal(raw, &parts[i]); err != nil {
			return LogEntry{}, err
		}
		impact, _ := json.Marshal(e.Impact)
		parts[i]["impactId"] = impact
	}
	top := hoist(parts)
	out := make([]json.RawMessage, len(parts))
	for i, p := range parts {
		if impact, ok := p["impactId"]; ok && len(p) == 1 {
			out[i] = impact // nothing but its impact: the id
			continue
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return LogEntry{}, err
		}
		out[i] = raw
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return LogEntry{}, err
	}
	top[batchKey] = raw
	raw, err = json.Marshal(top)
	return LogEntry{ID: id, Change: first.Change, Type: LogImpact + "." + string(first.Op), Flow: first.Flow, Execution: first.Execution,
		By: first.By, At: first.At, Causation: first.Causation, Payload: raw}, err
}

// hoist takes out of the parts what every one of them holds alike, a key or, in an object every part holds, a key of
// it, and returns it (unhoist puts it back).
func hoist(parts []map[string]json.RawMessage) map[string]json.RawMessage {
	top := map[string]json.RawMessage{}
	for k, v := range parts[0] {
		if k == "impactId" {
			continue
		}
		shared := true
		for _, p := range parts[1:] {
			if w, ok := p[k]; !ok || !bytes.Equal(v, w) {
				shared = false
				break
			}
		}
		if shared {
			top[k] = v
			for _, p := range parts {
				delete(p, k)
			}
			continue
		}
		// an object in every part: what its keys hold alike
		subs := make([]map[string]json.RawMessage, len(parts))
		for i, p := range parts {
			if json.Unmarshal(p[k], &subs[i]) != nil || subs[i] == nil {
				subs = nil
				break
			}
		}
		if len(subs) < 2 {
			continue
		}
		in := hoist(subs)
		if len(in) == 0 {
			continue
		}
		raw, _ := json.Marshal(in)
		top[k] = raw
		for i, p := range parts {
			p[k], _ = json.Marshal(subs[i])
		}
	}
	return top
}

// unhoist puts back into a part what hoist took out of it.
func unhoist(top, part map[string]json.RawMessage) error {
	for k, v := range top {
		w, ok := part[k]
		if !ok {
			part[k] = v
			continue
		}
		var in, sub map[string]json.RawMessage
		if err := json.Unmarshal(v, &in); err != nil {
			return err
		}
		if err := json.Unmarshal(w, &sub); err != nil {
			return err
		}
		if err := unhoist(in, sub); err != nil {
			return err
		}
		raw, err := json.Marshal(sub)
		if err != nil {
			return err
		}
		part[k] = raw
	}
	return nil
}

// ImpactEventsOf is the impact events of a log entry: the one it records, or the ones it carries (ImpactBatchEntry),
// one per node of a commit (CommitEntry).
func ImpactEventsOf(l LogEntry) ([]ImpactEvent, error) {
	if l.Type == LogImpact+"."+string(ImpactCommitted) && bytes.Contains(l.Payload, []byte(`"`+nodesKey+`"`)) {
		return commitEvents(l)
	}
	if !bytes.Contains(l.Payload, []byte(`"`+batchKey+`"`)) {
		e, err := ImpactEventOf(l)
		if err != nil {
			return nil, err
		}
		return []ImpactEvent{e}, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(l.Payload, &top); err != nil {
		return nil, err
	}
	raw, ok := top[batchKey]
	if !ok {
		e, err := ImpactEventOf(l)
		if err != nil {
			return nil, err
		}
		return []ImpactEvent{e}, nil
	}
	delete(top, batchKey)
	var raws []json.RawMessage
	if err := json.Unmarshal(raw, &raws); err != nil {
		return nil, err
	}
	out := make([]ImpactEvent, len(raws))
	for i, r := range raws {
		var p map[string]json.RawMessage
		if len(r) > 0 && r[0] == '"' {
			p = map[string]json.RawMessage{"impactId": r} // a part holding its impact only
		} else if err := json.Unmarshal(r, &p); err != nil {
			return nil, err
		}
		if err := unhoist(top, p); err != nil {
			return nil, err
		}
		payload, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		part := l
		part.ID, part.Payload = PartID(l.ID, i), payload
		if part.Subject == "" {
			var impact ChangeImpactID
			_ = json.Unmarshal(p["impactId"], &impact)
			part.Subject = string(impact)
		}
		if out[i], err = ImpactEventOf(part); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ImpactEventOf is the impact event of a log entry recording one: its payload with the envelope of the entry. The draft
// of a BaseCopy event holds its base only until the graph hydrates it (Hydrated). An entry carrying several events is
// read by ImpactEventsOf.
func ImpactEventOf(l LogEntry) (ImpactEvent, error) {
	var in impactPayloadIn
	if err := json.Unmarshal(l.Payload, &in); err != nil {
		return in.ImpactEvent, err
	}
	e := in.ImpactEvent
	if s := in.State; s != nil {
		st := s.ChangeImpact
		if s.Key != nil {
			st.Key = *s.Key
		} else if e.Draft != nil {
			st.Key = e.Draft.Key
		}
		if s.Type != nil {
			st.Type = *s.Type
		} else if e.Draft != nil {
			st.Type = e.Draft.Type
		}
		e.State = &st
	}
	switch {
	case len(in.Post) == 0:
		if n := e.draftNode(); n != "" {
			ref := DraftRef(n)
			e.Post = &ref
		}
	case !bytes.Equal(in.Post, nullPost):
		if err := json.Unmarshal(in.Post, &e.Post); err != nil {
			return e, err
		}
	}
	if e.ID == "" {
		e.ID = l.ID
	}
	if e.Change == "" {
		e.Change = l.Change
	}
	if e.Impact == "" {
		e.Impact = ChangeImpactID(l.Subject)
	}
	e.Seq = int(l.Seq)
	e.Flow, e.Execution, e.By, e.At, e.Causation = l.Flow, l.Execution, l.By, l.At, l.Causation
	if e.State != nil && e.State.ID == "" {
		e.State.ID = e.Impact
	}
	if d := e.Draft; d != nil {
		d.Change, d.Impact, d.Flow = e.Change, e.Impact, e.Flow
		if e.FromBase != nil && d.Base != nil {
			d.Node = d.Base.ID
		}
	}
	return e, nil
}

// A commit is one event of the log (ADR 0144 addendum): {"op": "committed", "commit", "branch", "nodes": [...]}, the
// nodes it commits, in order, valid for all of them at once. A node whose version is the change's own for its impact
// (ImpactEvent.Recorded) is its id, or {"id", "effect"}: node_version names the change, the impact and the entry, so
// the graph reads them back (Graph hydrates the entry: CommitNodes, WithCommitNodes); another node names its impact
// and its version too.
const nodesKey = "nodes"

// CommittedNode is a node a commit names: its version and impact, its effect (ImpactEvent.Effect). Version 0 and no
// impact: left to the version store.
type CommittedNode struct {
	ID      NodeID         `json:"id"`
	Impact  ChangeImpactID `json:"impactId,omitempty"`
	Version Version        `json:"version,omitempty"`
	Effect  *ImpactEffect  `json:"effect,omitempty"`
}

// Hydrated reports a node whose version and impact the entry names.
func (n CommittedNode) Hydrated() bool { return n.Version > 0 && n.Impact != "" }

// MarshalJSON writes a node naming nothing but itself as its id.
func (n CommittedNode) MarshalJSON() ([]byte, error) {
	if n.Impact == "" && n.Version == 0 && n.Effect == nil {
		return json.Marshal(string(n.ID))
	}
	type plain CommittedNode
	return json.Marshal(plain(n))
}

// UnmarshalJSON reads a node written as its id or whole.
func (n *CommittedNode) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		*n = CommittedNode{}
		return json.Unmarshal(raw, &n.ID)
	}
	type plain CommittedNode
	var p plain
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	*n = CommittedNode(p)
	return nil
}

// commitPayload is the payload of a commit.
type commitPayload struct {
	Op     ImpactOp        `json:"op"`
	Commit CommitID        `json:"commit,omitempty"`
	Branch string          `json:"branch,omitempty"`
	Nodes  []CommittedNode `json:"nodes"`
}

// CommitEntry is the log entry of a commit: the committed events of one commit (SameEntry, one commit and branch), in
// their order, under the id given.
func CommitEntry(id string, events []ImpactEvent) (LogEntry, error) {
	if len(events) == 0 {
		return LogEntry{}, fmt.Errorf("a commit names at least one node")
	}
	first := events[0]
	p := commitPayload{Op: ImpactCommitted, Commit: first.Commit, Branch: first.Branch, Nodes: make([]CommittedNode, len(events))}
	for i, e := range events {
		if !SameEntry(first, e) || e.Op != ImpactCommitted || e.Commit != first.Commit || e.Branch != first.Branch || e.Committed == nil {
			return LogEntry{}, fmt.Errorf("event %d (%s of %s) is not of the commit %s on %s", i, e.Op, e.Impact, first.Commit, first.Branch)
		}
		n := CommittedNode{ID: e.Committed.ID, Effect: e.Effect}
		if !e.Recorded {
			n.Impact, n.Version = e.Impact, e.Committed.Version
		}
		p.Nodes[i] = n
	}
	raw, err := json.Marshal(p)
	l := LogEntry{ID: id, Change: first.Change, Type: LogImpact + "." + string(ImpactCommitted), Flow: first.Flow, Execution: first.Execution,
		By: first.By, At: first.At, Causation: first.Causation, Payload: raw}
	if len(events) == 1 {
		l.Subject, l.Node = string(first.Impact), first.Committed.ID
	}
	return l, err
}

// CommitNodes is the nodes a commit entry names (ok false: the entry is no commit, or one written before commits were
// one event).
func CommitNodes(l LogEntry) ([]CommittedNode, bool, error) {
	if l.Type != LogImpact+"."+string(ImpactCommitted) || !bytes.Contains(l.Payload, []byte(`"`+nodesKey+`"`)) {
		return nil, false, nil
	}
	var p commitPayload
	if err := json.Unmarshal(l.Payload, &p); err != nil {
		return nil, false, err
	}
	return p.Nodes, true, nil
}

// WithCommitNodes is the commit entry naming the nodes given in place of its own (the graph hydrating it).
func WithCommitNodes(l LogEntry, nodes []CommittedNode) (LogEntry, error) {
	var p commitPayload
	if err := json.Unmarshal(l.Payload, &p); err != nil {
		return l, err
	}
	p.Nodes = nodes
	raw, err := json.Marshal(p)
	l.Payload = raw
	return l, err
}

// commitEvents reads a commit back as one committed event per node, with the ids PartID gives (the entry's own when it
// names one node). A node left to the version store has version 0 and, unless the entry is its own, no impact
// (Hydrated false).
func commitEvents(l LogEntry) ([]ImpactEvent, error) {
	var p commitPayload
	if err := json.Unmarshal(l.Payload, &p); err != nil {
		return nil, err
	}
	out := make([]ImpactEvent, len(p.Nodes))
	for i, n := range p.Nodes {
		id := PartID(l.ID, i)
		if len(p.Nodes) == 1 {
			id = l.ID
		}
		impact := n.Impact
		if impact == "" && len(p.Nodes) == 1 {
			impact = ChangeImpactID(l.Subject)
		}
		ref := NodeRef{ID: n.ID, Version: n.Version}
		out[i] = ImpactEvent{ID: id, Change: l.Change, Seq: int(l.Seq), Impact: impact, Op: ImpactCommitted, Flow: l.Flow, Execution: l.Execution,
			By: l.By, At: l.At, Causation: l.Causation, Committed: &ref, Effect: n.Effect, Commit: p.Commit, Branch: p.Branch, Recorded: !n.Hydrated()}
	}
	return out, nil
}
