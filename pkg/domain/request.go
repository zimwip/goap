package domain

import (
	"encoding/json"
	"slices"
	"time"
)

// RequestID identifies a request.
type RequestID string

// Request is the origin of a piece of work (ADR 0098): who asks for what, from where, when. It is an object of the change
// component, not of the graph, and may exist before any change; it is linked to the changes that answer it (many to
// many, RequestLink). Its text is the voice of the requester and is never rewritten.
type Request struct {
	ID    RequestID `json:"id"`
	Title string    `json:"title"`
	Text  string    `json:"text,omitempty"`
	// Requester is the subject who asked.
	Requester string `json:"requester"`
	// ProjectID is the project of the request, empty until it is triaged: a change linked to it is in it.
	ProjectID string        `json:"projectId,omitempty"`
	Origin    RequestOrigin `json:"origin"`
	// Status is the stored status; Delivered (derived) is reported by Request.Effective.
	Status    RequestStatus `json:"status"`
	CreatedAt time.Time     `json:"createdAt"`
	// Links are the changes the request is linked to (read with the request).
	Links []RequestLink `json:"links,omitempty"`
}

// RequestOrigin says where a request comes from: Kind (conversation, trigger, external, change, manual) and a reference
// in it (a conversation and message, a trigger, a ticket, a change).
type RequestOrigin struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
}

// Origins of a request.
const (
	OriginConversation = "conversation"
	OriginTrigger      = "trigger"
	OriginExternal     = "external"
	OriginChange       = "change"
	OriginManual       = "manual"
)

// RequestOrigins lists the origins of a request.
var RequestOrigins = []string{OriginConversation, OriginTrigger, OriginExternal, OriginChange, OriginManual}

// RequestStatus is where a request stands.
type RequestStatus string

const (
	// RequestOpen: asked, not triaged yet.
	RequestOpen RequestStatus = "open"
	// RequestTriaged: its project is known and it is linked to at least one change.
	RequestTriaged RequestStatus = "triaged"
	// RequestDelivered is derived: every change it is linked to is applied. Never stored.
	RequestDelivered RequestStatus = "delivered"
	// RequestClosed: the requester (or a triager) confirmed the need is met. Final.
	RequestClosed RequestStatus = "closed"
	// RequestRejected: it will not be worked on. Final.
	RequestRejected RequestStatus = "rejected"
	// RequestWithdrawn: the requester took it back. Final.
	RequestWithdrawn RequestStatus = "withdrawn"
)

// Final reports a status a request does not leave.
func (s RequestStatus) Final() bool {
	return s == RequestClosed || s == RequestRejected || s == RequestWithdrawn
}

// LinkRole says how a change answers a request.
type LinkRole string

const (
	// LinkOrigin: the change was created for the request.
	LinkOrigin LinkRole = "origin"
	// LinkAmends: the request changes the intent of a change in progress.
	LinkAmends LinkRole = "amends"
	// LinkCovers: the change answers the request, wholly or in part.
	LinkCovers LinkRole = "covers"
)

// ValidLinkRole reports a known role.
func ValidLinkRole(r LinkRole) bool { return r == LinkOrigin || r == LinkAmends || r == LinkCovers }

// RequestLink links a request to a change.
type RequestLink struct {
	Change  ChangeID  `json:"changeId"`
	Request RequestID `json:"requestId"`
	Role    LinkRole  `json:"role"`
	By      string    `json:"by,omitempty"`
	At      time.Time `json:"at"`
	// ChangeStatus is the status of the change, read with the link.
	ChangeStatus ChangeStatus `json:"changeStatus,omitempty"`
}

// Effective is the status of a request with the derived one: a triaged request whose linked changes are all applied
// is delivered.
func (r Request) Effective() RequestStatus {
	if r.Status != RequestTriaged || len(r.Links) == 0 {
		return r.Status
	}
	for _, l := range r.Links {
		if l.ChangeStatus != ChangeApplied {
			return r.Status
		}
	}
	return RequestDelivered
}

// RequestFilter selects requests. Empty fields select everything.
type RequestFilter struct {
	Requester string
	// Projects keeps the requests of these projects ("" the untriaged ones); nil: every project.
	Projects []string
	// Statuses keeps these effective statuses.
	Statuses []RequestStatus
	// Change keeps the requests linked to a change.
	Change ChangeID
	Limit  int
}

// Match tells whether a request is selected (Change and Limit aside).
func (f RequestFilter) Match(r Request) bool {
	return (f.Requester == "" || r.Requester == f.Requester) &&
		(f.Projects == nil || slices.Contains(f.Projects, r.ProjectID)) &&
		(len(f.Statuses) == 0 || slices.Contains(f.Statuses, r.Effective()))
}

// Kinds of the entries of the log of a request.
const (
	RequestCreated   = "created"
	RequestUpdated   = "updated"
	RequestLinked    = "linked"
	RequestUnlinked  = "unlinked"
	RequestMoved     = "moved"
	RequestStatusSet = "status"
)

// RequestEntry is an entry of the log of a request: what happened to it, by whom, with its details.
type RequestEntry struct {
	Seq     int64           `json:"seq"`
	Request RequestID       `json:"requestId"`
	Type    string          `json:"type"`
	By      string          `json:"by,omitempty"`
	At      time.Time       `json:"at"`
	Payload json.RawMessage `json:"payload,omitempty"`
}
