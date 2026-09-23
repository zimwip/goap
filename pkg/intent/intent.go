// Package intent implements the intent loop run before planning: a user
// request is ranked against the goals of a methodology until one goal is
// selected with enough confidence, asking clarification questions otherwise.
package intent

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// GoalInfo describes a goal to the ranker.
type GoalInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Examples    []string `json:"examples,omitempty"`
}

// Turn is one exchange of the intent dialogue.
type Turn struct {
	Role string `json:"role"` // user | assistant
	Text string `json:"text"`
}

// Candidate is a ranked goal.
type Candidate struct {
	Goal       string  `json:"goal"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason,omitempty"`
	// Agent and Methodology are filled by the engine for agent identification.
	Agent       string `json:"agent,omitempty"`
	Methodology string `json:"methodology,omitempty"`
}

// Ranker ranks goals for a dialogue. Candidates are returned by decreasing
// confidence.
type Ranker interface {
	Rank(ctx context.Context, turns []Turn, goals []GoalInfo) ([]Candidate, error)
}

// Clarifier optionally generates a clarification question.
type Clarifier interface {
	Clarify(ctx context.Context, turns []Turn, candidates []Candidate, goals []GoalInfo) (string, error)
}

// Session is the state of an intent dialogue.
type Session struct {
	Turns []Turn `json:"turns"`
	// Offered lists the goals proposed in the last clarification question, so
	// that an answer like "2" can select one.
	Offered []string `json:"offered,omitempty"`
}

// Resolution is the outcome of one resolution round.
type Resolution struct {
	Goal       string      `json:"goal,omitempty"`
	Question   string      `json:"question,omitempty"`
	Candidates []Candidate `json:"candidates"`
}

// Resolved reports whether a goal was selected.
func (r Resolution) Resolved() bool { return r.Goal != "" }

// Resolver drives the intent loop.
type Resolver struct {
	Ranker Ranker
	// Threshold is the minimum confidence of the selected goal (default 0.6).
	Threshold float64
	// Margin is the minimum gap with the second candidate (default 0.15).
	Margin float64
	// MaxTurns bounds clarification rounds; afterwards the best candidate is
	// selected if its confidence is positive (default 3).
	MaxTurns int
}

// Resolve runs one round: rank, then select or ask. The session is updated
// with the assistant question when one is asked.
func (r Resolver) Resolve(ctx context.Context, s *Session, goals []GoalInfo) (Resolution, error) {
	if len(goals) == 0 {
		return Resolution{}, fmt.Errorf("no goal to resolve")
	}
	threshold, margin, maxTurns := r.Threshold, r.Margin, r.MaxTurns
	if threshold == 0 {
		threshold = 0.6
	}
	if margin == 0 {
		margin = 0.15
	}
	if maxTurns == 0 {
		maxTurns = 3
	}
	if len(goals) == 1 {
		return Resolution{Goal: goals[0].Name, Candidates: []Candidate{{Goal: goals[0].Name, Confidence: 1, Reason: "single goal"}}}, nil
	}
	if g := pickOffered(s, goals); g != "" {
		s.Offered = nil
		return Resolution{Goal: g, Candidates: []Candidate{{Goal: g, Confidence: 1, Reason: "explicit choice"}}}, nil
	}
	cands, err := r.Ranker.Rank(ctx, s.Turns, goals)
	if err != nil {
		return Resolution{}, err
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Confidence > cands[j].Confidence })
	res := Resolution{Candidates: cands}
	if len(cands) > 0 {
		top := cands[0]
		second := 0.0
		if len(cands) > 1 {
			second = cands[1].Confidence
		}
		if top.Confidence >= threshold && top.Confidence-second >= margin {
			res.Goal = top.Goal
			s.Offered = nil
			return res, nil
		}
		if userTurns(s) > maxTurns && top.Confidence > 0 {
			res.Goal = top.Goal
			s.Offered = nil
			return res, nil
		}
	}
	res.Question, s.Offered = r.question(ctx, s, cands, goals)
	s.Turns = append(s.Turns, Turn{Role: "assistant", Text: res.Question})
	return res, nil
}

func (r Resolver) question(ctx context.Context, s *Session, cands []Candidate, goals []GoalInfo) (string, []string) {
	offered := make([]string, 0, 3)
	for _, c := range cands {
		if len(offered) == 3 {
			break
		}
		if c.Confidence > 0 {
			offered = append(offered, c.Goal)
		}
	}
	if len(offered) < 2 {
		offered = offered[:0]
		for _, g := range goals {
			offered = append(offered, g.Name)
		}
	}
	if c, ok := r.Ranker.(Clarifier); ok {
		if q, err := c.Clarify(ctx, s.Turns, cands, goals); err == nil && q != "" {
			return q, offered
		}
	}
	desc := map[string]string{}
	for _, g := range goals {
		desc[g.Name] = g.Description
	}
	var b strings.Builder
	b.WriteString("I'm not sure of the goal. Would you like to:\n")
	for i, g := range offered {
		fmt.Fprintf(&b, "%d) %s\n", i+1, desc[g])
	}
	b.WriteString("Reply with the number or rephrase.")
	return b.String(), offered
}

func pickOffered(s *Session, goals []GoalInfo) string {
	if len(s.Turns) == 0 {
		return ""
	}
	last := s.Turns[len(s.Turns)-1]
	if last.Role != "user" {
		return ""
	}
	answer := strings.TrimSpace(strings.TrimRight(last.Text, ".)"))
	if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(s.Offered) {
		return s.Offered[n-1]
	}
	for _, g := range goals {
		// goal names may be qualified ("methodology/agent/goal"): accept any suffix
		if strings.EqualFold(answer, g.Name) || strings.HasSuffix(strings.ToLower(g.Name), "/"+strings.ToLower(answer)) {
			return g.Name
		}
	}
	return ""
}

func userTurns(s *Session) int {
	n := 0
	for _, t := range s.Turns {
		if t.Role == "user" {
			n++
		}
	}
	return n
}
