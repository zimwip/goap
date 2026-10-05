// Package verify is the use case of independent verification (ADR 0075): a result produced is not a result accepted.
// An action declares how its effect is verified (methodology.Action.Verify: the kind of oracle, and that the verifier
// is not the producer); the engine records the states of what an action run produced as facts of the change
// (fact.verification, ADR 0030), folded here; Policy is the rule the graph asks before a review (domain.ReviewPolicy,
// as pkg/decision is the policy of the decision points, ADR 0067): the verifier is not the producer.
//
// The states, per effect (a change impact) of an action run: produced (the action ended), verified (its oracle
// judged), then accepted, accepted_with_reserve (only with a derogation in force, pkg/risk) or rejected (the review
// settled). The graph and the domain model import nothing from here (pkg/layering); the composition roots plug the
// policy on the graph, explicitly (Graph.ReviewPolicy), and call Register where such items are written or read.
//
// Data of a verification item: state, action (the producing action), impact (the change impact, one per item except
// on produced), impacts (the change impacts a produced item covers), oracle, independent (the verifier must differ
// from the producer), model (the alias of a model producer, when known), by (the verifier).
package verify

import (
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// KindVerification is the kind of the items that record a state of verification: fact.verification in the log.
const KindVerification domain.ItemKind = "verification"

// States of a produced effect.
const (
	Produced            = "produced"
	Verified            = "verified"
	Accepted            = "accepted"
	AcceptedWithReserve = "accepted_with_reserve"
	Rejected            = "rejected"
)

// Keys of the data of a verification item.
const (
	KeyState       = "state"
	KeyAction      = "action"
	KeyImpact      = "impact"
	KeyImpacts     = "impacts"
	KeyOracle      = "oracle"
	KeyIndependent = "independent"
	KeyModel       = "model"
	KeyBy          = "by"
	// KeyDerogation is the key of the derogation an accepted_with_reserve entry stands on (pkg/risk, ADR 0075 §2).
	KeyDerogation = "derogation"
)

// Register makes the verification item kind known to the graph (domain.RegisterItemKind). The services that write or
// read such items call it at start, and so does a test that builds them; it may be called any number of times.
func Register() { domain.RegisterItemKind(KindVerification, validate) }

// ValidState reports whether s is a state of verification.
func ValidState(s string) bool {
	return slices.Contains([]string{Produced, Verified, Accepted, AcceptedWithReserve, Rejected}, s)
}

func validate(it domain.ChangeItem) error {
	state, _ := it.Data[KeyState].(string)
	if !ValidState(state) {
		return fmt.Errorf("verification item: unknown state %q", state)
	}
	if a, _ := it.Data[KeyAction].(string); a == "" {
		return fmt.Errorf("verification item requires data.action")
	}
	if state == AcceptedWithReserve {
		if d, _ := it.Data[KeyDerogation].(string); d == "" {
			return fmt.Errorf("verification item %s requires data.derogation: no reserve without a derogation", state)
		}
	}
	if state != Produced {
		if i, _ := it.Data[KeyImpact].(string); i == "" {
			return fmt.Errorf("verification item %s requires data.impact", state)
		}
	}
	return nil
}

// Subject is the verification of one effect of an action run: the state its last entry put it in.
type Subject struct {
	Execution   string
	Action      string
	Impact      string // the change impact; empty for a run that produced no change impact
	Oracle      string
	Independent bool
	Model       string
	State       string
	Producer    string // who produced it
	By          string // the last one who judged it
	Derogation  string // the derogation an accepted_with_reserve stands on
	Seq         int    // position of the last entry in the items
}

// Open reports whether the effect still waits for a verdict.
func (s Subject) Open() bool { return s.State == Produced || s.State == Verified }

// Subjects folds the verification items of a change, in the order they were written, into the state of each effect.
// It is pure: the same items give the same subjects. A produced entry creates one subject per change impact it covers
// (one without impact when it covers none); the later entries of an impact move its subject.
func Subjects(items []domain.ChangeItem) []Subject {
	var out []Subject
	byImpact := map[string]int{} // impact id -> index in out
	for n, it := range items {
		if it.Kind != KindVerification {
			continue
		}
		state, _ := it.Data[KeyState].(string)
		str := func(k string) string { s, _ := it.Data[k].(string); return s }
		if state == Produced {
			base := Subject{Execution: it.Execution, Action: str(KeyAction), Oracle: str(KeyOracle), Model: str(KeyModel), State: Produced, Producer: it.ProducedBy, Seq: n}
			base.Independent, _ = it.Data[KeyIndependent].(bool)
			impacts := strList(it.Data[KeyImpacts])
			if len(impacts) == 0 {
				out = append(out, base)
				continue
			}
			for _, id := range impacts {
				s := base
				s.Impact = id
				if i, ok := byImpact[id]; ok {
					out[i] = s // produced again (a relaunch): starts over
				} else {
					byImpact[id] = len(out)
					out = append(out, s)
				}
			}
			continue
		}
		if i, ok := byImpact[str(KeyImpact)]; ok && ValidState(state) {
			out[i].State, out[i].Seq = state, n
			out[i].Derogation = ""
			if state == AcceptedWithReserve {
				out[i].Derogation = str(KeyDerogation)
			}
			if by := str(KeyBy); by != "" {
				out[i].By = by
			}
		}
	}
	return out
}

// Independent returns the verification an action run declared for a change impact when it asked for a verifier that
// differs from the producer, and whether there is one.
func Independent(items []domain.ChangeItem, impact domain.ChangeImpactID) (Subject, bool) {
	for _, s := range Subjects(items) {
		if s.Impact == string(impact) && s.Independent {
			return s, true
		}
	}
	return Subject{}, false
}

func strList(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		var out []string
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Impacts returns the change impacts a verification item covers (data.impacts, and data.impact).
func Impacts(it domain.ChangeItem) []string {
	out := slices.Clone(strList(it.Data[KeyImpacts]))
	if s, _ := it.Data[KeyImpact].(string); s != "" {
		out = append(out, s)
	}
	return out
}
