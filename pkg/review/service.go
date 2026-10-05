package review

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
)

// Port is what the operations need of the graph: the change (with its items), the write of items, and the batch of
// reviews that applies a submission atomically (graph.Graph and graphsvc.Client implement it).
type Port interface {
	Change(ctx context.Context, id domain.ChangeID) (domain.Change, error)
	AddItems(ctx context.Context, id domain.ChangeID, items []domain.ChangeItem) ([]domain.ChangeItem, error)
	ImpactNodeReviewBatch(ctx context.Context, id domain.ChangeID, b domain.ReviewBatch) ([]domain.ChangeImpact, error)
}

// Actor is who acts on a review: the principal's subject, and whether it may act on the reviews of others (an
// administrator; the caller decides, the use case knows no role).
type Actor struct {
	Subject string
	Admin   bool
}

// adminRole is the platform role of an administrator (access.RoleAdmin; this package cannot import pkg/access).
const adminRole = "admin"

// ActorOf is the actor of a call: the principal of the context, an administrator when it holds the admin role or is a
// platform service. A service that knows more (the floor of the authorizer) sets Admin itself.
func ActorOf(ctx context.Context) Actor {
	p := authz.From(ctx)
	return Actor{Subject: p.Subject, Admin: p.System() || slices.Contains(p.Roles, adminRole)}
}

// Edit is a change to an open review. Comment replaces the global comment when set; Remove then Add change the set of
// impacts (an impact added joins with no outcome); Entries set the comment / outcome of entries, after the set changed.
type Edit struct {
	Comment *string
	Remove  []string
	Add     []string
	Entries []EntryEdit
}

// EntryEdit sets the comment and / or the outcome of the entry of an impact.
type EntryEdit struct {
	Impact  string
	Comment *string
	Outcome *string // accept, reject, or "" to take the decision back
}

// Service is the use case: it reads the reviews of a change, checks them and writes them as items.
type Service struct {
	Port Port
	// Now and NewKey are the clock and the key generator; nil: time.Now and a random key.
	Now    func() time.Time
	NewKey func() string
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) newKey() string {
	if s.NewKey != nil {
		return s.NewKey()
	}
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return "REV-" + strings.ToUpper(hex.EncodeToString(b))
}

// flowArg is the flow a call names for a resolved flow: the main flow explicitly, so that no active option takes it.
func flowArg(flow string) string {
	if flow == "" {
		return domain.MainFlow
	}
	return flow
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Open starts a review on a flow ("": the active option, else the main flow) with its global comment.
func (s Service) Open(ctx context.Context, change domain.ChangeID, flow, comment string, who Actor) (Review, error) {
	c, err := s.Port.Change(ctx, change)
	if err != nil {
		return Review{}, err
	}
	flow = c.ResolveFlow(flow)
	r := Review{Key: s.newKey(), Flow: flow, Comment: strings.TrimSpace(comment), Status: Open, By: who.Subject, Entries: []Entry{}}
	return s.write(ctx, change, r, who)
}

// Update changes an open review: only its author or an administrator, and only while it is open. The impacts it adds
// must await review on its flow and be in no other open review of the flow.
func (s Service) Update(ctx context.Context, change domain.ChangeID, key string, e Edit, who Actor) (Review, error) {
	c, r, err := s.load(ctx, change, key, who)
	if err != nil {
		return Review{}, err
	}
	if e.Comment != nil {
		r.Comment = strings.TrimSpace(*e.Comment)
	}
	for _, imp := range e.Remove {
		if _, ok := r.Entry(imp); !ok {
			return Review{}, invalid("impact %s is not in review %s", imp, key)
		}
		r.Entries = slices.DeleteFunc(slices.Clone(r.Entries), func(x Entry) bool { return x.Impact == imp })
	}
	if len(e.Add) > 0 {
		reviews := Reviews(c.Items)
		awaiting := map[string]domain.ChangeImpact{}
		for _, cn := range Awaiting(c, r.Flow, reviews, key) {
			awaiting[string(cn.ID)] = cn
		}
		r.Entries = slices.Clone(r.Entries)
		for _, imp := range e.Add {
			if _, ok := r.Entry(imp); ok {
				continue // already in: adding is idempotent
			}
			if _, ok := awaiting[imp]; !ok {
				return Review{}, s.whyNotAwaiting(c, r.Flow, reviews, key, imp)
			}
			r.Entries = append(r.Entries, Entry{Impact: imp})
		}
	}
	for _, ee := range e.Entries {
		i := slices.IndexFunc(r.Entries, func(x Entry) bool { return x.Impact == ee.Impact })
		if i < 0 {
			return Review{}, invalid("impact %s is not in review %s", ee.Impact, key)
		}
		r.Entries = slices.Clone(r.Entries)
		if ee.Comment != nil {
			r.Entries[i].Comment = strings.TrimSpace(*ee.Comment)
		}
		if ee.Outcome != nil {
			if o := *ee.Outcome; o != "" && o != Accept && o != Reject {
				return Review{}, invalid("outcome of %s must be %s or %s", ee.Impact, Accept, Reject)
			}
			r.Entries[i].Outcome = *ee.Outcome
		}
	}
	return s.write(ctx, change, r, who)
}

// whyNotAwaiting is the refusal of an impact that cannot join a review.
func (s Service) whyNotAwaiting(c domain.Change, flow string, reviews []Review, key, impact string) error {
	i := slices.IndexFunc(c.Nodes, func(cn domain.ChangeImpact) bool { return string(cn.ID) == impact })
	if i < 0 {
		return fmt.Errorf("%w: change impact %s is not in change %s", ErrNotFound, impact, c.ID)
	}
	for _, o := range reviews {
		if o.Open() && o.Flow == flow && o.Key != key {
			if _, ok := o.Entry(impact); ok {
				return fmt.Errorf("%w: change impact %s (%s) is already in the open review %s", ErrConflict, impact, c.Nodes[i].Key, o.Key)
			}
		}
	}
	return fmt.Errorf("%w: change impact %s (%s) does not await review on this flow (%s)", ErrConflict, impact, c.Nodes[i].Key, statusOn(c.Nodes[i], flow))
}

// Discard drops an open review: a review that was never submitted stays in the log, final.
func (s Service) Discard(ctx context.Context, change domain.ChangeID, key string, who Actor) (Review, error) {
	_, r, err := s.load(ctx, change, key, who)
	if err != nil {
		return Review{}, err
	}
	r.Status = Discarded
	return s.write(ctx, change, r, who)
}

// Submit applies every entry as a review of its change impact, by the submitter, with the entry's comment and the
// global one: in one transaction, all of them or none. A refusal names the entry and leaves the review open. The
// review item becomes submitted, final.
func (s Service) Submit(ctx context.Context, change domain.ChangeID, key string, who Actor) (Review, error) {
	_, r, err := s.load(ctx, change, key, who)
	if err != nil {
		return Review{}, err
	}
	if len(r.Entries) == 0 {
		return Review{}, invalid("review %s has no entry", key)
	}
	b := domain.ReviewBatch{ID: key, Flow: flowArg(r.Flow), By: who.Subject}
	for _, e := range r.Entries {
		if !e.Decided() {
			return Review{}, invalid("impact %s has no outcome yet", e.Impact)
		}
		status := domain.ReviewAccepted
		if e.Outcome == Reject {
			status = domain.ReviewRejected
		}
		comment := EffectiveComment(r.Comment, e.Comment)
		if comment == "" {
			return Review{}, invalid("impact %s needs a comment, its own or the review's", e.Impact)
		}
		b.Verdicts = append(b.Verdicts, domain.ImpactVerdict{Impact: domain.ChangeImpactID(e.Impact), Status: status, Comment: comment})
	}
	r.Status, r.SubmittedAt = Submitted, s.now()
	it := domain.ChangeItem{Kind: KindReview, Flow: flowArg(r.Flow), Data: dataOf(r), ProducedBy: who.Subject}
	b.Item = &it
	if _, err := s.Port.ImpactNodeReviewBatch(ctx, change, b); err != nil {
		return Review{}, err
	}
	c, err := s.Port.Change(ctx, change)
	if err != nil {
		return Review{}, err
	}
	out, _ := Find(c.Items, key)
	return out, nil
}

// load reads a review for a change of it: it must exist, be open and be the actor's (or the actor an administrator).
func (s Service) load(ctx context.Context, change domain.ChangeID, key string, who Actor) (domain.Change, Review, error) {
	c, err := s.Port.Change(ctx, change)
	if err != nil {
		return c, Review{}, err
	}
	r, ok := Find(c.Items, key)
	if !ok {
		return c, r, fmt.Errorf("%w: review %s of change %s", ErrNotFound, key, change)
	}
	if !who.Admin && who.Subject != r.By {
		return c, r, fmt.Errorf("%w: review %s belongs to %s", ErrForbidden, key, r.By)
	}
	if !r.Open() {
		return c, r, fmt.Errorf("%w: review %s is %s: it is final", ErrConflict, key, r.Status)
	}
	return c, r, nil
}

// write stores a version of the review as an item and returns what the change then folds it to.
func (s Service) write(ctx context.Context, change domain.ChangeID, r Review, who Actor) (Review, error) {
	it := domain.ChangeItem{Kind: KindReview, Flow: flowArg(r.Flow), Data: dataOf(r), ProducedBy: who.Subject}
	items, err := s.Port.AddItems(ctx, change, []domain.ChangeItem{it})
	if err != nil {
		return Review{}, err
	}
	// the review as the item put it: the write is the truth
	out := Reviews(items)
	if len(out) == 0 {
		return r, nil
	}
	got := out[0]
	got.Versions = r.Versions + 1
	return got, nil
}
