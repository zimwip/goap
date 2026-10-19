package domain

import (
	"encoding/json"

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
		d.Links = append(d.Links, DraftLink{ID: BaseLinkID(e.ID, l.ID), Type: l.Type, To: to, Properties: cloneProps(l.Properties)})
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
			p.Draft = &draftPayload{Draft: Draft{Base: d.Base, Execution: d.Execution}}
		} else {
			p.Draft = &draftPayload{Draft: *d}
		}
	}
	return json.Marshal(p)
}

// ImpactEventOf is the impact event of a log entry: its payload with the envelope of the entry. The draft of a BaseCopy
// event holds its base only until the graph hydrates it (Hydrated).
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
