package methodology

import (
	"fmt"
	"strings"
)

// Review declares a step a review (ADR 0130): the step rates a subject of the change (an assessment item, ADR 0110 §3)
// and its verdict advises, it never decides. The review is part of the process it sits in, explicitly: the methodology
// says what is reviewed, where, and whether the review starts by itself.
//
// An automatic review starts without a person launching it: on the events that change its subject (the change
// created, the subject edited, a remark about it) the engine runs the step on the change (a reaction, ADR 0110 §2),
// and only those runs and a run asking for the step itself carry it out: the other runs of its process wait for its
// exit instead of rating the subject a second time (Compiled.AutomaticReview).
type Review struct {
	// Subject is what the review rates: description, title, intent, impact:<node key>, item:<record key or id>
	// (the subjects of contracts/assess).
	Subject string `yaml:"subject" json:"subject"`
	// Automatic starts the step by itself on the events of its subject; a step that declares on: reacts to those
	// instead.
	Automatic bool `yaml:"automatic,omitempty" json:"automatic,omitempty"`
}

// The subjects of a review (those of contracts/assess, which the methodology does not import).
const (
	ReviewDescription = "description"
	ReviewTitle       = "title"
	ReviewIntent      = "intent"
	reviewImpact      = "impact:"
	reviewItem        = "item:"
)

// headerFields are the fields of the change header each header subject reads.
var headerFields = map[string][]string{
	ReviewDescription: {"title", "intent"},
	ReviewTitle:       {"title"},
	ReviewIntent:      {"intent"},
}

// validSubject reports whether a subject names something a review can rate.
func validSubject(s string) bool {
	if _, ok := headerFields[s]; ok {
		return true
	}
	for _, p := range []string{reviewImpact, reviewItem} {
		if rest, ok := strings.CutPrefix(s, p); ok && rest != "" {
			return true
		}
	}
	return false
}

// Subscriptions are the events an automatic review of a header subject reacts to: the change created, the fields of
// its subject edited (superseding the rating in flight: it would rate a text that is gone) and a remark of a person
// about the subject. Nil for the other subjects, whose step declares its own on.
func (r Review) Subscriptions() []Subscription {
	fields, ok := headerFields[r.Subject]
	if !ok {
		return nil
	}
	edited := make([]string, len(fields))
	for i, f := range fields {
		edited[i] = fmt.Sprintf("%q in event.fields", f)
	}
	return []Subscription{
		{Event: "change.created"},
		{Event: "change.edited", Filter: strings.Join(edited, " || "), Supersede: true},
		{Event: "change.item_added", Filter: fmt.Sprintf(`event.items.exists(i, i.type == "comment" && i.data.about == %q)`, r.Subject)},
	}
}

// ReactsTo is what the step reacts to: its own on, else the events of its automatic review.
func (s Step) ReactsTo() []Subscription {
	if len(s.On) > 0 || s.Review == nil || !s.Review.Automatic {
		return s.On
	}
	return s.Review.Subscriptions()
}

// checkReview checks the review of a step: a subject a review can rate, something that does the step, events to
// start an automatic one on.
func (s *compileState) checkReview(path string, st Step) {
	r := st.Review
	if r == nil {
		return
	}
	switch {
	case !validSubject(r.Subject):
		s.add(path+".review.subject", "a review rates description, title, intent, impact:<node key> or item:<record key or id>, not %q", r.Subject)
	case st.Method() == MethodManual && r.Automatic:
		s.add(path+".review.automatic", "an automatic review is done by an action or a method: a manual step waits for a person")
	case r.Automatic && len(st.ReactsTo()) == 0:
		s.add(path+".review.automatic", "an automatic review of %q starts on events the step declares (on): only the header subjects have default ones", r.Subject)
	}
}

// AutomaticReview reports, for an action the compiler generated for a step of an automatic review (or one of its
// sub-steps), the path of that review step. A run carries such an action out only when its goal is the review step
// or one of its sub-steps (its reaction, or a person asking for it): the other runs of the process wait for its exit.
func (c *Compiled) AutomaticReview(action string) (string, bool) {
	step, ok := c.automaticReviews[action]
	return step, ok
}

// Carries reports whether a run towards goal carries out the automatic review step: the goal is the step or a step
// within it.
func Carries(goal, step string) bool {
	return goal == step || strings.HasPrefix(goal, step+"/")
}

// compileAutomaticReviews checks the reviews of the steps of the processes and methods and records the actions
// generated for the automatic ones (for a review within another, the outer one).
func (s *compileState) compileAutomaticReviews() {
	var reviews []string
	var steps func(list []Step, path, prefix string)
	steps = func(list []Step, path, prefix string) {
		for i, st := range list {
			sp, full := fmt.Sprintf("%s.steps[%d]", path, i), prefix+"/"+st.Name
			s.checkReview(sp, st)
			if st.Review != nil && st.Review.Automatic {
				reviews = append(reviews, full)
			}
			steps(st.Steps, sp, full)
		}
	}
	for i, p := range s.m.Processes {
		steps(p.Steps, fmt.Sprintf("processes[%d]", i), p.Name)
	}
	for i, me := range s.m.Methods {
		steps(me.Steps, fmt.Sprintf("methods[%d]", i), me.Name)
	}
	if len(reviews) == 0 {
		return
	}
	s.automaticReviews = map[string]string{}
	for _, a := range s.procs.actions {
		for _, step := range reviews { // outer steps come first
			if Carries(a.Step, step) {
				s.automaticReviews[a.Name] = step
				break
			}
		}
	}
}
