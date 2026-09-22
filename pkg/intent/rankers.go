package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/zimwip/goap/pkg/llm"
)

// Lexical ranks goals by token overlap between the user turns and the goal
// name, description and examples. Deterministic; used in dev and tests.
type Lexical struct{}

// Rank implements Ranker.
func (Lexical) Rank(_ context.Context, turns []Turn, goals []GoalInfo) ([]Candidate, error) {
	var text strings.Builder
	for _, t := range turns {
		if t.Role == "user" {
			text.WriteString(" ")
			text.WriteString(t.Text)
		}
	}
	utter := normalize(text.String())
	words := tokens(utter)
	scores := make([]float64, len(goals))
	total := 0.0
	for i, g := range goals {
		vocab := map[string]bool{}
		// the technical goal name is left out: identifiers like "prepare_change"
		// would match everyday words (an exact name is handled by the resolver)
		for w := range tokens(normalize(g.Description + " " + strings.Join(g.Examples, " "))) {
			vocab[w] = true
		}
		for w := range words {
			if vocab[w] {
				scores[i]++
			}
		}
		for _, ex := range g.Examples {
			if e := normalize(ex); e != "" && strings.Contains(utter, e) {
				scores[i] += 3
			}
		}
		total += scores[i]
	}
	out := make([]Candidate, len(goals))
	for i, g := range goals {
		out[i] = Candidate{Goal: g.Name, Confidence: scores[i] / (total + 1), Reason: fmt.Sprintf("score %.0f", scores[i])}
	}
	return out, nil
}

var stopwords = map[string]bool{"les": true, "des": true, "une": true, "est": true, "que": true, "qui": true, "pour": true,
	"dans": true, "sur": true, "avec": true, "the": true, "and": true, "for": true, "sans": true, "par": true, "pas": true, "quel": true}

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 3 && !stopwords[w] {
			out[w] = true
		}
	}
	return out
}

func normalize(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	r, _, _ := transform.String(t, strings.ToLower(s))
	r = strings.NewReplacer("'", " ", "’", " ").Replace(r)
	return strings.Join(strings.Fields(r), " ")
}

// LLMRanker ranks goals with a language model.
type LLMRanker struct {
	Client llm.Client
	Model  string
}

const rankSystem = `You classify a user request against the goals of an enterprise methodology.
Answer with a single JSON object: {"candidates":[{"goal":"<name>","confidence":<0..1>,"reason":"..."}],"question":"<clarification question in the user's language, or empty>"}.
Confidences express how sure you are that the user wants this goal; they need not sum to 1. List every goal.`

// Rank implements Ranker.
func (r LLMRanker) Rank(ctx context.Context, turns []Turn, goals []GoalInfo) ([]Candidate, error) {
	var out struct {
		Candidates []Candidate `json:"candidates"`
	}
	if err := r.ask(ctx, turns, goals, &out); err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, g := range goals {
		known[g.Name] = true
	}
	var cands []Candidate
	for _, c := range out.Candidates {
		if known[c.Goal] {
			cands = append(cands, c)
		}
	}
	return cands, nil
}

// Clarify implements Clarifier.
func (r LLMRanker) Clarify(ctx context.Context, turns []Turn, _ []Candidate, goals []GoalInfo) (string, error) {
	var out struct {
		Question string `json:"question"`
	}
	if err := r.ask(ctx, turns, goals, &out); err != nil {
		return "", err
	}
	return out.Question, nil
}

func (r LLMRanker) ask(ctx context.Context, turns []Turn, goals []GoalInfo, v any) error {
	g, _ := json.MarshalIndent(goals, "", "  ")
	var dialog strings.Builder
	for _, t := range turns {
		fmt.Fprintf(&dialog, "%s: %s\n", t.Role, t.Text)
	}
	model := r.Model
	if model == "" {
		model = "fast"
	}
	resp, err := r.Client.Complete(ctx, llm.Request{
		Model: model, System: rankSystem, JSON: true, MaxTokens: 2048,
		Messages: []llm.Message{{Role: "user", Content: "Goals:\n" + string(g) + "\n\nDialogue:\n" + dialog.String()}},
	})
	if err != nil {
		return err
	}
	return llm.DecodeJSON(resp.Text, v)
}
