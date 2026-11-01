// Package review is the review object of a change (ADR 0080): a reviewer builds a review up, with a global comment and
// one entry per change impact (its own comment and outcome), then submits it; the submission decides every entry as an
// ordinary review of the impact, all of them or none. The review is an item of the change of the built-in kind
// domain.KindReview (ADR 0105: every verdict on an impact is made by a submitted review, the graph writes the ones it
// makes itself), each item a delta of the review of its data.key (ADR 0105 addendum: the gesture only, the verdicts
// derived from the submission by the view of the change); Reviews folds the items,
// Service (service.go) is the use case over a Port that builds a review up and submits it. pkg/graph and pkg/domain
// know nothing of this package: the record format is domain's, the mechanism the graph's (domain.ReviewBatch).
//
// Data of a review item (domain.ReviewDelta): key, and what the gesture sets: status (open, then submitted or discarded:
// both final), comment (the global comment), by (the author, when opened), remove (impacts), entries ([{impact,
// comment, outcome}]: added, or their comment and outcome set).
package review

import (
	"errors"
	"slices"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
)

// KindReview is the kind of the items that hold a review of a change: built in (domain.KindReview, ADR 0105).
const KindReview = domain.TypeReview

// Statuses of a review: it is open while the reviewer builds it, then submitted or discarded, which are final.
const (
	Open      = domain.ReviewStatusOpen
	Submitted = domain.ReviewStatusSubmitted
	Discarded = domain.ReviewStatusDiscarded
)

// Outcomes of an entry: the verdict on its change impact. Empty while the reviewer has not decided.
const (
	Accept = domain.ReviewOutcomeAccept
	Reject = domain.ReviewOutcomeReject
)

// The refusals of the operations; the services map them as the graph's own (internal/rpcerr).
var (
	ErrInvalid   = errors.New("invalid review")
	ErrConflict  = errors.New("review conflict")
	ErrNotFound  = errors.New("review not found")
	ErrForbidden = authz.ErrForbidden
)

// Entry is the review of one change impact: its own comment and outcome.
type Entry = domain.ReviewEntry

// Review is the current version of a review of the change.
type Review struct {
	Key         string    `json:"key"`
	Flow        string    `json:"flow,omitempty"`
	Comment     string    `json:"comment,omitempty"`
	Status      string    `json:"status"`
	Entries     []Entry   `json:"entries"`
	By          string    `json:"by,omitempty"`
	SubmittedAt time.Time `json:"submittedAt,omitempty"`
	// Item is the item holding this version, Versions how many versions the key has.
	Item     domain.ItemID `json:"item"`
	Versions int           `json:"versions"`
}

// Open reports whether the reviewer may still change the review.
func (r Review) Open() bool { return r.Status == Open }

// Ready reports whether the review may be submitted: open, at least one entry, an outcome on every one.
func (r Review) Ready() bool {
	if !r.Open() || len(r.Entries) == 0 {
		return false
	}
	return slices.IndexFunc(r.Entries, func(e Entry) bool { return !e.Decided() }) < 0
}

// Entry returns the entry of a change impact.
func (r Review) Entry(impact string) (Entry, bool) {
	i := slices.IndexFunc(r.Entries, func(e Entry) bool { return e.Impact == impact })
	if i < 0 {
		return Entry{}, false
	}
	return r.Entries[i], true
}

// EffectiveComment is the comment the review of an impact keeps when the review is submitted: the entry's own, then
// the global one, so that each impact's review keeps both. It is empty only when both are.
func EffectiveComment(global, entry string) string {
	return domain.ReviewEffectiveComment(global, entry)
}

// Reviews folds the items of a change into its reviews (domain.ReviewFold: each item a delta of the review of its
// key), in the order the keys appeared. A review that is submitted or discarded is final: a later delta of its key is
// ignored. A rejected or superseded item is not a delta. It is pure.
func Reviews(items []domain.ChangeItem) []Review {
	states := domain.ReviewsOf(items).Reviews()
	out := make([]Review, 0, len(states))
	for _, s := range states {
		out = append(out, reviewOf(s))
	}
	return out
}

func reviewOf(s domain.ReviewState) Review {
	return Review{Key: s.Key, Flow: s.Flow, Comment: s.Comment, Status: s.Status, Entries: slices.Clone(s.Entries), By: s.By,
		SubmittedAt: s.SubmittedAt, Item: s.Item, Versions: s.Versions}
}

// Find returns the review of a key.
func Find(items []domain.ChangeItem, key string) (Review, bool) {
	for _, r := range Reviews(items) {
		if r.Key == key {
			return r, true
		}
	}
	return Review{}, false
}

// chain lists the flows from flow up to the main flow ("" included, last).
func chain(c domain.Change, flow string) []string {
	out := []string{flow}
	for hops := 0; flow != "" && hops < 64; hops++ {
		f, ok := c.Flow(flow)
		if !ok {
			break
		}
		flow = f.Parent
		out = append(out, flow)
	}
	return out
}

// statusOn is the review a change impact has for a flow: the last review made on that flow, else the one of the
// impact (the main flow's).
func statusOn(cn domain.ChangeImpact, flow string) domain.NodeReview {
	if flow != "" {
		for i := len(cn.Reviews) - 1; i >= 0; i-- {
			if r := cn.Reviews[i]; r.Flow == flow && !r.Superseded {
				return r.Status
			}
		}
	}
	return cn.Review
}

// Awaiting returns the change impacts a review of a flow may take: those the flow sees (not superseded, declared on the
// flow or its parents) whose review is proposed and that no other open review of the same flow holds. Pass the reviews
// of the change (Reviews); except is the key of a review whose own entries do not count (the one being edited).
func Awaiting(c domain.Change, flow string, reviews []Review, except string) []domain.ChangeImpact {
	flows := chain(c, flow)
	held := map[string]bool{}
	for _, r := range reviews {
		if r.Open() && r.Flow == flow && r.Key != except {
			for _, e := range r.Entries {
				held[e.Impact] = true
			}
		}
	}
	var out []domain.ChangeImpact
	for _, cn := range c.Nodes {
		if cn.Superseded || !slices.Contains(flows, cn.Flow) || statusOn(cn, flow) != domain.ReviewProposed || held[string(cn.ID)] {
			continue
		}
		out = append(out, cn)
	}
	return out
}
