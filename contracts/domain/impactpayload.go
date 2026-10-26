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
		State: base.State, Owner: base.Owner, Properties: cloneProps(base.Properties), Base: &b}
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
	return e.FromBase == nil || (e.Draft != nil && e.Draft.Key != "")
}

// absent shadows a field of an embedded value in a payload: a nil pointer, omitted.
type absent *struct{}

// impactPayload is an ImpactEvent as the log stores it: the envelope left to the columns.
type impactPayload struct {
	ImpactEvent
	ID        absent        `json:"id,omitempty"`
	Change    absent        `json:"changeId,omitempty"`
	Seq       absent        `json:"seq,omitempty"`
	Impact    absent        `json:"impactId,omitempty"`
	Flow      absent        `json:"flow,omitempty"`
	Execution absent        `json:"execution,omitempty"`
	By        absent        `json:"by,omitempty"`
	At        absent        `json:"at,omitempty"`
	Causation absent        `json:"causation,omitempty"`
	State     any           `json:"state,omitempty"` // *statePayload, else the change impact whole
	Draft     *draftPayload `json:"draft,omitempty"`
}

// statePayload is the change impact a proposed or created event adds, its id being the event's impact.
type statePayload struct {
	ChangeImpact
	ID absent `json:"id,omitempty"`
}

// draftPayload is a draft in a payload: its change, impact and flow are the event's (applyDraft sets them).
type draftPayload struct {
	Draft
	Change absent `json:"changeId,omitempty"`
	Impact absent `json:"impactId,omitempty"`
	Flow   absent `json:"flow,omitempty"`
}

func marshalImpact(e ImpactEvent) ([]byte, error) {
	p := impactPayload{ImpactEvent: e}
	if e.State != nil {
		if e.State.ID == e.Impact {
			p.State = &statePayload{ChangeImpact: *e.State}
		} else {
			p.State = e.State // another impact's (none today): kept whole
		}
	}
	if d := e.Draft; d != nil {
		if e.FromBase != nil {
			// the copy of a version: the version says the rest
			p.Draft = &draftPayload{Draft: Draft{Base: d.Base, Execution: d.Execution, MergedWith: d.MergedWith}}
		} else {
			p.Draft = &draftPayload{Draft: *d}
		}
	}
	return json.Marshal(p)
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
// the first event's. ImpactEventsOf reads them back, with the ids PartID gives.
func ImpactBatchEntry(id string, events []ImpactEvent) (LogEntry, error) {
	if len(events) == 0 {
		return LogEntry{}, fmt.Errorf("an entry carries at least one event")
	}
	first := events[0]
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
	raw, err := json.Marshal(parts)
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

// ImpactEventsOf is the impact events of a log entry: the one it records, or the ones it carries (ImpactBatchEntry).
func ImpactEventsOf(l LogEntry) ([]ImpactEvent, error) {
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
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, err
	}
	out := make([]ImpactEvent, len(parts))
	for i, p := range parts {
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
	var e ImpactEvent
	if err := json.Unmarshal(l.Payload, &e); err != nil {
		return e, err
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
