package domain

import "testing"

// What a version does to its node: a link is its type and its target node, another version of the target the same link.
func TestEffectOf(t *testing.T) {
	need, test := NodeRef{ID: "N", Version: 1}, NodeRef{ID: "T", Version: 1}
	pre := Node{ID: "R", Version: 1, State: "draft", Properties: map[string]any{"title": "a", "old": 1}}
	preLinks := []Link{{Type: "satisfies", To: need}, {Type: "verifiedBy", To: test, Properties: map[string]any{"w": 1}}}
	post := Node{ID: "R", Version: 2, State: "approved", Properties: map[string]any{"title": "b", "new": 2}}
	postLinks := []Link{{Type: "satisfies", To: NodeRef{ID: "N", Version: 3}}, {Type: "verifiedBy", To: test, Properties: map[string]any{"w": 2}}, {Type: "refines", To: need}}
	if got, want := EffectOf(&pre, preLinks, post, postLinks), (ImpactEffect{Kind: EffectModified, Properties: 3, State: true, LinksAdded: 1, LinksChanged: 1}); got != want {
		t.Fatalf("modified: %+v, want %+v", got, want)
	}
	if got, want := EffectOf(nil, nil, post, postLinks), (ImpactEffect{Kind: EffectCreated, Properties: 2, LinksAdded: 3}); got != want {
		t.Fatalf("created: %+v, want %+v", got, want)
	}
	tomb := Node{ID: "R", Version: 3, Deleted: true, Properties: post.Properties}
	if got, want := EffectOf(&post, postLinks, tomb, nil), (ImpactEffect{Kind: EffectDeleted, LinksRemoved: 3}); got != want {
		t.Fatalf("deleted: %+v, want %+v", got, want)
	}
	if got, want := EffectOf(&tomb, nil, pre, preLinks), (ImpactEffect{Kind: EffectRestored, Properties: 2, LinksAdded: 2}); got != want {
		t.Fatalf("restored: %+v, want %+v", got, want)
	}
}

// The counters of a change fold its impacts of the main flow: the committed ones by their effect (by their intent when
// none was recorded), the others by review.
func TestStatsOf(t *testing.T) {
	v := &NodeRef{ID: "X", Version: 2}
	impacts := ImpactsOf([]ChangeImpact{
		{ID: "1", Intent: IntentCreated, Committed: v, Effect: ImpactEffect{Kind: EffectCreated, Properties: 2, LinksAdded: 1}},
		{ID: "2", Intent: IntentModified, Committed: v, Effect: ImpactEffect{Kind: EffectRestored, Requalified: true, LinksAdded: 2}},
		{ID: "3", Intent: IntentModified, Committed: v, Effect: ImpactEffect{Kind: EffectDeleted, LinksRemoved: 1}},
		{ID: "4", Intent: IntentModified, Committed: v, Effect: ImpactEffect{Kind: EffectModified, State: true, LinksChanged: 1}},
		{ID: "5", Intent: IntentCreated, Committed: v},
		{ID: "6", Intent: IntentModified, Review: ReviewAccepted},
		{ID: "7", Intent: IntentModified, Review: ReviewRejected},
		{ID: "8", Intent: IntentCreated, Review: ReviewProposed},
		{ID: "9", Intent: IntentCreated, Review: ReviewProposed, Flow: "opt"},
		{ID: "10", Intent: IntentCreated, Committed: v, Superseded: true},
	})
	want := ChangeStats{Created: 2, Modified: 1, Deleted: 1, Restored: 1, Requalified: 1, Properties: 2, States: 1, LinksAdded: 3, LinksRemoved: 1,
		LinksChanged: 1, Proposed: 1, Accepted: 1, Rejected: 1}
	s := StatsOf(impacts)
	if s != want {
		t.Fatalf("%+v, want %+v", s, want)
	}
	if s.Nodes() != 5 || s.Pending() != 3 || s.Committed().Pending() != 0 {
		t.Fatalf("nodes %d, pending %d", s.Nodes(), s.Pending())
	}
}
