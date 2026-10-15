// Package assess is the assessment of what a person wrote on a change (ADR 0110): an action rates a subject (the
// description of the change) with an item of kind assessment, and the person discusses the rating with items of kind
// comment, the thread of the subject. It is a use case like pkg/risk: pkg/graph and pkg/domain know nothing of it, the
// conditions derive from it (the CEL variable assessments, the library assessments of pkg/condition).
package assess

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// The kinds of item of an assessment.
const (
	// TypeAssessment is a rating of a subject: each item is the whole record of its key (data.key), the latest one in
	// effect is the rating.
	TypeAssessment domain.ItemType = "assessment"
	// TypeComment is a message of a person about a subject (data.about), in the thread of its assessment.
	TypeComment domain.ItemType = "comment"
)

// The subjects of a rating (data.key of an assessment, data.about of a comment): the description of the change (its
// title and its intent), its title, its intent, an impact by the key of its node ("impact:<key>") and an item by the
// key of its record or its id ("item:<key or id>").
const (
	SubjectDescription = "description"
	SubjectTitle       = "title"
	SubjectIntent      = "intent"
	ImpactPrefix       = "impact:"
	ItemPrefix         = "item:"
)

// MaxScore is the best score; a score goes from 0 to MaxScore.
const MaxScore = 5

// Verdicts of an assessment: of a text (clear, unclear, insufficient) or of a claim made about its subject, such as
// a suspected impact (confirmed, refuted).
const (
	VerdictClear        = "clear"
	VerdictUnclear      = "unclear"
	VerdictInsufficient = "insufficient"
	VerdictConfirmed    = "confirmed"
	VerdictRefuted      = "refuted"
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
	// Rated is what the rating is about: the text of the subject or its fingerprint (Fingerprint).
	Rated string `json:"rated"`
	// Current: the subject still reads as rated (a rating of another text says nothing of this one); a subject the
	// change no longer has is not current.
	Current bool `json:"current"`
	// Answered: no comment about the subject came after the rating.
	Answered bool          `json:"answered"`
	Item     domain.ItemID `json:"item"`
	By       string        `json:"by,omitempty"`
	Versions int           `json:"versions"`
}

// Message is a message of the thread of a subject: a comment of a person, or a rating (its reply).
type Message struct {
	Type domain.ItemType `json:"type"`
	Item domain.ItemID   `json:"item"`
	Text string          `json:"text"`
	By   string          `json:"by,omitempty"`
	// Score is the score of a rating.
	Score int `json:"score,omitempty"`
}

// Register makes the kinds known to the graph (domain.RegisterItemKind); the services writing or reading such items
// call it at start, and so does a test building them. It may be called any number of times.
func Register() {
	domain.RegisterItemType(TypeAssessment, validateAssessment)
	domain.RegisterItemType(TypeComment, validateComment)
	domain.DescribeItemType(TypeAssessment, "assess", "A rating of a subject of the change (score, verdict, findings, suggestion): each item is the whole record of its key (ADR 0110).")
	domain.DescribeItemType(TypeComment, "assess", "A message of a person about a subject, in the thread of its assessment (ADR 0110).")
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
	case "", VerdictClear, VerdictUnclear, VerdictInsufficient, VerdictConfirmed, VerdictRefuted:
	default:
		return fmt.Errorf("assessment verdict must be %s, %s, %s, %s or %s", VerdictClear, VerdictUnclear, VerdictInsufficient, VerdictConfirmed, VerdictRefuted)
	}
	return nil
}

func validateComment(it domain.ChangeItem) error {
	if str(it.Data, "about") == "" || strings.TrimSpace(str(it.Data, "text")) == "" {
		return fmt.Errorf("comment item requires data.about (its subject) and data.text")
	}
	return nil
}

// Text is the text of a subject as the change has it now: the description is the title and the intent; an impact
// is its node as the change has it (key, type, state, properties, outgoing links), an item its data (the latest version
// of a record). False when the change has no such subject.
func Text(bb domain.Blackboard, key string) (string, bool) {
	c := bb.Change
	switch {
	case key == SubjectDescription:
		return strings.TrimSpace(c.Title) + "\n\n" + strings.TrimSpace(c.Intent), true
	case key == SubjectTitle:
		return strings.TrimSpace(c.Title), true
	case key == SubjectIntent:
		return strings.TrimSpace(c.Intent), true
	case strings.HasPrefix(key, ImpactPrefix):
		nodeKey := strings.TrimPrefix(key, ImpactPrefix)
		for i := len(c.Nodes) - 1; i >= 0; i-- {
			if n := c.Nodes[i]; n.Key == nodeKey && !n.Superseded {
				return impactText(bb, n), true
			}
		}
	case strings.HasPrefix(key, ItemPrefix):
		ref := strings.TrimPrefix(key, ItemPrefix)
		for i := len(c.Items) - 1; i >= 0; i-- {
			it := c.Items[i]
			if it.Type == TypeAssessment || it.Type == TypeComment || !c.Active(it.ID) {
				continue
			}
			if string(it.ID) == ref || str(it.Data, "key") == ref {
				return canonical(map[string]any{"type": string(it.Type), "target": it.Target, "data": it.Data}), true
			}
		}
	}
	return "", false
}

// impactText is the canonical text of the node of an impact as the change has it.
func impactText(bb domain.Blackboard, n domain.ChangeImpact) string {
	ref := n.Post
	if ref == nil {
		ref = n.Pre
	}
	doc := map[string]any{"key": n.Key, "type": n.Type, "intent": string(n.Intent)}
	if ref != nil {
		v := bb.Nodes[*ref]
		links := make([]string, 0, len(v.Out))
		for _, l := range v.Out {
			links = append(links, l.Type+">"+string(l.To.ID))
		}
		slices.Sort(links)
		doc["state"], doc["props"], doc["links"] = v.State, v.Properties, links
	}
	return canonical(doc)
}

func canonical(v any) string {
	b, _ := json.Marshal(v) // maps are marshalled with sorted keys
	return string(b)
}

// Fingerprint is the short fingerprint of the text of a subject (FNV-1a, 32 bits, hexadecimal): what a rater copies in
// `rated` when the text is long, shown by the brief next to each subject.
func Fingerprint(text string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	return fmt.Sprintf("%08x", h.Sum32())
}

// FingerprintOf is the fingerprint of a subject of the change, "" when the change has no such subject.
func FingerprintOf(bb domain.Blackboard, key string) string {
	if text, ok := Text(bb, key); ok {
		return Fingerprint(text)
	}
	return ""
}

// rates reports whether a rating made on rated is about text.
func rates(rated, text string) bool {
	rated = strings.TrimSpace(rated)
	return rated == strings.TrimSpace(text) || rated == Fingerprint(text)
}

// Assessments returns the latest rating of each subject, in the order the subjects were first rated.
func Assessments(bb domain.Blackboard) []Assessment {
	c := bb.Change
	var order []string
	last := map[string]domain.ChangeItem{}
	at := map[string]int{}
	versions := map[string]int{}
	lastComment := map[string]int{}
	for i, it := range c.Items {
		if !c.Active(it.ID) || it.Status == domain.ItemRejected {
			continue
		}
		switch it.Type {
		case TypeComment:
			lastComment[str(it.Data, "about")] = i
		case TypeAssessment:
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
		if text, ok := Text(bb, key); ok {
			a.Current = rates(a.Rated, text)
		}
		i, commented := lastComment[key]
		a.Answered = !commented || i < at[key]
		out = append(out, a)
	}
	return out
}

// Of returns the latest rating of a subject.
func Of(bb domain.Blackboard, key string) (Assessment, bool) {
	for _, a := range Assessments(bb) {
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
		case it.Type == TypeComment && str(it.Data, "about") == key:
			out = append(out, Message{Type: TypeComment, Item: it.ID, Text: str(it.Data, "text"), By: str(it.Data, "by")})
		case it.Type == TypeAssessment && str(it.Data, "key") == key:
			text := str(it.Data, "reply")
			if text == "" {
				text = str(it.Data, "verdict")
			}
			out = append(out, Message{Type: TypeAssessment, Item: it.ID, Text: text, By: it.ProducedBy, Score: num(it.Data, "score")})
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
