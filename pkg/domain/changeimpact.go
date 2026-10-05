package domain

import (
	"fmt"
	"strings"
	"time"
)

// ChangeImpactID identifies a ChangeImpact.
type ChangeImpactID string

// NodeIntent is what a change means to do with a node (ADR 0024). A removal is
// not an intent: removing a child is a modification of its parent.
type NodeIntent string

const (
	IntentCreated  NodeIntent = "created"
	IntentModified NodeIntent = "modified"
)

// NodeReview is the review state of a change impact.
type NodeReview string

const (
	ReviewProposed NodeReview = "proposed"
	ReviewAccepted NodeReview = "accepted"
	ReviewRejected NodeReview = "rejected"
)

// Review is one entry of the review history of a change impact.
type Review struct {
	Status  NodeReview `json:"status"`
	By      string     `json:"by,omitempty"`
	Comment string     `json:"comment"`
	At      time.Time  `json:"at"`
	// Flow is the flow branch the review was made on ("" = the main flow), Execution the
	// action run that made it; Superseded marks a review replaced by an adopted flow (ADR 0025).
	Flow       string `json:"flow,omitempty"`
	Execution  string `json:"execution,omitempty"`
	Superseded bool   `json:"superseded,omitempty"`
	// ReviewID names the review (an opaque id, ADR 0080) a batch of verdicts was submitted in: every review the batch
	// wrote carries it, so the audit groups them. Empty for a review made on its own.
	ReviewID string `json:"reviewId,omitempty"`
}

// ChangeImpact is the link from a change to a node: the node version the change
// reads (Pre), the version it produces on the change branch (Post, empty until
// realized) and the one that landed on the target branch (Landed), with the
// reason why (Rationale).
type ChangeImpact struct {
	ID        ChangeImpactID `json:"id"`
	Key       string         `json:"key"`
	Type      string         `json:"type"`
	Intent    NodeIntent     `json:"intent"`
	Rationale string         `json:"rationale"`
	// Pre is the released version of the reference baseline (nil when Intent is created).
	Pre *NodeRef `json:"pre,omitempty"`
	// Post is the draft of the node while the change works (a draft reference: the node, Version 0, ADR 0079), the
	// version written once the change is committed; nil while the change impact is only planned.
	Post *NodeRef `json:"post,omitempty"`
	// Landed is the version on the target branch once the change is applied.
	Landed  *NodeRef   `json:"landed,omitempty"`
	Review  NodeReview `json:"review"`
	Reviews []Review   `json:"reviews,omitempty"`
	// Via is the change impact of the parent that realizes the removal of this node.
	Via ChangeImpactID `json:"via,omitempty"`
	// Recheck marks a rationale written against a version that is no longer the head.
	Recheck     bool     `json:"recheck,omitempty"`
	ProducedBy  string   `json:"producedBy,omitempty"`
	DerivedFrom []ItemID `json:"derivedFrom,omitempty"`
	Execution   string   `json:"execution,omitempty"`
	// Items are the impact and proposal items this change impact stands for (the bridge
	// of ADR 0024): set on change impacts derived from items, empty on the ones written directly.
	Items []ItemID `json:"items,omitempty"`
	// Flow is the flow branch that declared the change impact ("" = the main flow): a candidate
	// until the flow is adopted. Superseded marks a change impact replaced by an adopted flow (ADR 0025).
	Flow       string    `json:"flow,omitempty"`
	Superseded bool      `json:"superseded,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Planned reports whether the change impact has no post yet: its node is not checked out, no draft exists.
func (c ChangeImpact) Planned() bool { return c.Post == nil }

// Drafted reports whether the node of the change impact has a draft: Post is then the draft reference (Version 0, ADR
// 0079); once the change landed it is the version written.
func (c ChangeImpact) Drafted() bool { return c.Post != nil && c.Post.IsDraft() }

// Validate checks the shape of a change impact.
func (c ChangeImpact) Validate() error {
	if c.Key == "" || c.Type == "" {
		return fmt.Errorf("change impact needs a key and a type")
	}
	if strings.TrimSpace(c.Rationale) == "" {
		return fmt.Errorf("change impact %s needs a rationale", c.Key)
	}
	switch c.Intent {
	case IntentCreated:
		if c.Pre != nil {
			return fmt.Errorf("change impact %s: a created node has no pre version", c.Key)
		}
	case IntentModified:
		if c.Pre == nil || c.Pre.Version == 0 {
			return fmt.Errorf("change impact %s: a modified node needs its pre version", c.Key)
		}
		if c.Post != nil && (c.Post.ID != c.Pre.ID || (!c.Post.IsDraft() && c.Post.Version <= c.Pre.Version)) {
			return fmt.Errorf("change impact %s: post %s is not a successor of pre %s", c.Key, c.Post, c.Pre)
		}
	default:
		return fmt.Errorf("change impact %s: unknown intent %q", c.Key, c.Intent)
	}
	if c.Landed != nil && c.Post == nil {
		return fmt.Errorf("change impact %s: landed without post", c.Key)
	}
	switch c.Review {
	case ReviewProposed, ReviewAccepted, ReviewRejected:
	default:
		return fmt.Errorf("change impact %s: unknown review %q", c.Key, c.Review)
	}
	return nil
}
