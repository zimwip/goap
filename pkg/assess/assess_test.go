package assess

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func board() domain.Blackboard {
	ref := domain.NodeRef{ID: "n1"}
	return domain.Blackboard{
		Change: domain.Change{Title: "Login", Intent: "let users sign in",
			Nodes: []domain.ChangeImpact{{ID: "i1", Key: "REQ-1", Type: "alm@Requirement", Intent: domain.IntentCreated, Post: &ref}},
			Items: []domain.ChangeItem{{ID: "a1", Type: domain.TypeArtifact, Target: "note", Data: map[string]any{"key": "spec", "text": "v1"}}}},
		Nodes: map[domain.NodeRef]domain.NodeView{ref: {Node: domain.Node{ID: "n1", Key: "REQ-1", Properties: map[string]any{"title": "Sign in"}}}},
	}
}

func rating(key, rated string, score int) domain.ChangeItem {
	return domain.ChangeItem{ID: domain.ItemID("r-" + key + rated), Type: TypeAssessment, Data: map[string]any{"key": key, "score": score, "rated": rated}}
}

func TestSubjectsAndFingerprints(t *testing.T) {
	bb := board()
	for _, key := range []string{SubjectDescription, SubjectTitle, SubjectIntent, ImpactPrefix + "REQ-1", ItemPrefix + "spec", ItemPrefix + "a1"} {
		if text, ok := Text(bb, key); !ok || text == "" {
			t.Fatalf("%s: no text", key)
		}
	}
	if _, ok := Text(bb, ImpactPrefix+"NOPE"); ok {
		t.Fatal("an impact the change does not have has no text")
	}
	if d, _ := Text(bb, SubjectDescription); d != "Login\n\nlet users sign in" {
		t.Fatalf("description %q", d)
	}

	// a rating by the fingerprint and one by the whole text are both current, until the subject changes
	item, _ := Text(bb, ItemPrefix+"spec")
	bb.Change.Items = append(bb.Change.Items, rating(ImpactPrefix+"REQ-1", FingerprintOf(bb, ImpactPrefix+"REQ-1"), 4), rating(ItemPrefix+"spec", item, 2))
	for _, key := range []string{ImpactPrefix + "REQ-1", ItemPrefix + "spec"} {
		if a, ok := Of(bb, key); !ok || !a.Current {
			t.Fatalf("%s: current rating expected, got %+v", key, a)
		}
	}
	bb.Nodes[domain.NodeRef{ID: "n1"}] = domain.NodeView{Node: domain.Node{ID: "n1", Key: "REQ-1", Properties: map[string]any{"title": "Sign in with SSO"}}}
	bb.Change.Items = append(bb.Change.Items, domain.ChangeItem{ID: "a2", Type: domain.TypeArtifact, Target: "note", Data: map[string]any{"key": "spec", "text": "v2"}})
	for _, key := range []string{ImpactPrefix + "REQ-1", ItemPrefix + "spec"} {
		if a, _ := Of(bb, key); a.Current {
			t.Fatalf("%s: the subject changed, the rating is stale", key)
		}
	}
	if Fingerprint("abc") != "1a47e90b" {
		t.Fatalf("FNV-1a 32 of abc: %s", Fingerprint("abc"))
	}
}
