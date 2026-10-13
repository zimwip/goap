// Package assess is the assessment of what a person wrote on a change (ADR 0110): an action rates a subject (the
// description of the change) with an item of kind assessment, and the person discusses the rating with items of kind
// comment, the thread of the subject. It is a use case like pkg/risk: pkg/graph and pkg/domain know nothing of it, the
// conditions derive from it (the CEL variable assessments, the library assessments of pkg/condition).
package assess

import (
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// The kinds of item of an assessment.
const (
	// KindAssessment is a rating of a subject: each item is the whole record of its key (data.key), the latest one in
	// effect is the rating.
	KindAssessment domain.ItemKind = "assessment"
	// KindComment is a message of a person about a subject (data.about), in the thread of its assessment.
	KindComment domain.ItemKind = "comment"
)

// SubjectDescription is the subject of the rating of the description of a change: its title and its intent.
const SubjectDescription = "description"

// MaxScore is the best score; a score goes from 0 to MaxScore.
const MaxScore = 5

// Verdicts of an assessment.
const (
	VerdictClear        = "clear"
	VerdictUnclear      = "unclear"
	VerdictInsufficient = "insufficient"
)

// Finding is one remark of an assessment.
type Finding struct {
	Text     string `json:"text"`
	Severity string `json:"severity,omitempty"`
}

// Assessment is the latest rating of a subject.
type Assessment struct {
	Key      string    `json:"key"`
	Score    int       `json:"score"`
	Verdict  string    `json:"verdict,omitempty"`
	Findings []Finding `json:"findings,omitempty"`
	// Suggestion is a wording the rater proposes, Reply what it says to the person (its answer in the thread).
	Suggestion string `json:"suggestion,omitempty"`
	Reply      string `json:"reply,omitempty"`
	// Rated is the text of the subject the rating is about.
	Rated string `json:"rated"`
	// Current: the subject still reads as rated (a rating of another text says nothing of this one).
	Current bool `json:"current"`
	// Answered: no comment about the subject came after the rating.
	Answered bool          `json:"answered"`
	Item     domain.ItemID `json:"item"`
	By       string        `json:"by,omitempty"`
	Versions int           `json:"versions"`
}

// Message is a message of the thread of a subject: a comment of a person, or a rating (its reply).
type Message struct {
	Kind domain.ItemKind `json:"kind"`
	Item domain.ItemID   `json:"item"`
	Text string          `json:"text"`
	By   string          `json:"by,omitempty"`
	// Score is the score of a rating.
	Score int `json:"score,omitempty"`
}

// Register makes the kinds known to the graph (domain.RegisterItemKind); the services writing or reading such items
// call it at start, and so does a test building them. It may be called any number of times.
func Register() {
	domain.RegisterItemKind(KindAssessment, validateAssessment)
	domain.RegisterItemKind(KindComment, validateComment)
}

func validateAssessment(it domain.ChangeItem) error {
	d := it.Data
	if str(d, "key") == "" {
		return fmt.Errorf("assessment item requires data.key, the subject it rates")
	}
	if raw, ok := d["score"]; !ok || raw == nil {
		return fmt.Errorf("assessment item requires data.score")
	}
	if s := num(d, "score"); s < 0 || s > MaxScore {
		return fmt.Errorf("assessment score must be between 0 and %d", MaxScore)
	}
	switch str(d, "verdict") {
	case "", VerdictClear, VerdictUnclear, VerdictInsufficient:
	default:
		return fmt.Errorf("assessment verdict must be %s, %s or %s", VerdictClear, VerdictUnclear, VerdictInsufficient)
	}
	return nil
}

func validateComment(it domain.ChangeItem) error {
	if str(it.Data, "about") == "" || strings.TrimSpace(str(it.Data, "text")) == "" {
		return fmt.Errorf("comment item requires data.about (its subject) and data.text")
	}
	return nil
}

// Text is the text of a subject of the change as it is now: the description is the title and the intent; "" for a
// subject the change does not hold as text.
func Text(c domain.Change, key string) (string, bool) {
	switch key {
	case SubjectDescription:
		return strings.TrimSpace(c.Title) + "\n\n" + strings.TrimSpace(c.Intent), true
	case "title":
		return strings.TrimSpace(c.Title), true
	case "intent":
		return strings.TrimSpace(c.Intent), true
	}
	return "", false
}

// Assessments returns the latest rating of each subject, in the order the subjects were first rated.
func Assessments(c domain.Change) []Assessment {
	var order []string
	last := map[string]domain.ChangeItem{}
	at := map[string]int{}
	versions := map[string]int{}
	lastComment := map[string]int{}
	for i, it := range c.Items {
		if !c.Active(it.ID) || it.Status == domain.ItemRejected {
			continue
		}
		switch it.Kind {
		case KindComment:
			lastComment[str(it.Data, "about")] = i
		case KindAssessment:
			key := str(it.Data, "key")
			if key == "" {
				continue
			}
			if _, seen := last[key]; !seen {
				order = append(order, key)
			}
			last[key], at[key] = it, i
			versions[key]++
		}
	}
	out := make([]Assessment, 0, len(order))
	for _, key := range order {
		it := last[key]
		d := it.Data
		a := Assessment{Key: key, Score: num(d, "score"), Verdict: str(d, "verdict"), Findings: findings(d["findings"]),
			Suggestion: str(d, "suggestion"), Reply: str(d, "reply"), Rated: str(d, "rated"), Item: it.ID, By: it.ProducedBy, Versions: versions[key]}
		if text, ok := Text(c, key); ok {
			a.Current = strings.TrimSpace(a.Rated) == text
		} else {
			a.Current = true
		}
		i, commented := lastComment[key]
		a.Answered = !commented || i < at[key]
		out = append(out, a)
	}
	return out
}

// Of returns the latest rating of a subject.
func Of(c domain.Change, key string) (Assessment, bool) {
	for _, a := range Assessments(c) {
		if a.Key == key {
			return a, true
		}
	}
	return Assessment{}, false
}

// Thread returns the messages about a subject in the order they were written: the comments of the persons and the
// ratings (their reply, else their verdict).
func Thread(c domain.Change, key string) []Message {
	var out []Message
	for _, it := range c.Items {
		if !c.Active(it.ID) || it.Status == domain.ItemRejected {
			continue
		}
		switch {
		case it.Kind == KindComment && str(it.Data, "about") == key:
			out = append(out, Message{Kind: KindComment, Item: it.ID, Text: str(it.Data, "text"), By: str(it.Data, "by")})
		case it.Kind == KindAssessment && str(it.Data, "key") == key:
			text := str(it.Data, "reply")
			if text == "" {
				text = str(it.Data, "verdict")
			}
			out = append(out, Message{Kind: KindAssessment, Item: it.ID, Text: text, By: it.ProducedBy, Score: num(it.Data, "score")})
		}
	}
	return out
}

func findings(v any) []Finding {
	var out []Finding
	list, _ := v.([]any)
	for _, f := range list {
		switch x := f.(type) {
		case string:
			out = append(out, Finding{Text: x})
		case map[string]any:
			out = append(out, Finding{Text: str(x, "text"), Severity: str(x, "severity")})
		}
	}
	return out
}

func str(d map[string]any, k string) string {
	s, _ := d[k].(string)
	return s
}

func num(d map[string]any, k string) int {
	switch v := d[k].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}
