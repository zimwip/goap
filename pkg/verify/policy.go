package verify

import (
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
)

// Policy is the review rule of the platform (domain.ReviewPolicy, ADR 0075): when the action run that produced a change
// impact declared an independent verification (a produced entry with independent set), the reviewer is not the producer.
// Principals are compared (the reviewer's and the impact's ProducedBy); when the producer is a model, its alias is kept
// in the produced entry (data.model) and a reviewer that names the same alias is refused too. A review of an effect
// that declared no verification is not constrained, as before. Policy holds no state.
type Policy struct{}

var _ domain.ReviewPolicy = Policy{}

// Review refuses a review by the producer of an effect that asked for an independent verifier.
func (Policy) Review(r domain.ReviewRequest) error {
	s, ok := Independent(r.Items, r.Impact.ID)
	if !ok {
		return nil
	}
	if r.Reviewer == "" {
		return nil
	}
	if r.Reviewer == r.Impact.ProducedBy || r.Reviewer == s.Producer || (s.Model != "" && r.Reviewer == s.Model) {
		return fmt.Errorf("%s produced %s and may not verify it: its verification is independent", r.Reviewer, r.Impact.Key)
	}
	return nil
}
