// Package eval is the offline evaluation of LLM work (ADR 0099): cases (a prompt, mocked tools, what is expected) run
// against a model through the same tool loop as an engine LLM action, scored on their answer, their tool trajectory
// and, optionally, by a model judging against a rubric. Each case runs several times: the pass rate, tokens and
// latency are the measure of a prompt or a model change.
package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zimwip/goap/contracts/llm"
)

// Case is one evaluation: what is sent, the tools the model may call (mocked: they answer what the case says) and
// what the answer must satisfy.
type Case struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	// Model is the alias or "provider/model" (default "default"); a run may override it.
	Model string `yaml:"model,omitempty"`
	// Prompt "engine" puts the system text of the engine's LLM actions (the item protocol) before System.
	Prompt    string `yaml:"prompt,omitempty"`
	System    string `yaml:"system,omitempty"`
	Messages  []Turn `yaml:"messages"`
	JSON      bool   `yaml:"json,omitempty"`
	MaxTokens int    `yaml:"maxTokens,omitempty"`
	// MaxRounds bounds the tool rounds (default DefaultMaxRounds).
	MaxRounds int    `yaml:"maxRounds,omitempty"`
	Tools     []Tool `yaml:"tools,omitempty"`
	Expect    Expect `yaml:"expect"`
	Judge     *Judge `yaml:"judge,omitempty"`
	// Runs is how many times the case runs (default 1); MinPassRate the share of passing runs for the case to pass
	// (default 1).
	Runs        int     `yaml:"runs,omitempty"`
	MinPassRate float64 `yaml:"minPassRate,omitempty"`
	// File is where the case was read from.
	File string `yaml:"-"`
}

// Turn is a message of the conversation sent.
type Turn struct {
	Role    string `yaml:"role"`
	Content string `yaml:"content"`
}

// Tool is a mocked tool: its declaration and what it answers. The first of Responses whose Args are a subset of the
// call's arguments answers; else Result, or Error when set.
type Tool struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description,omitempty"`
	InputSchema map[string]any `yaml:"inputSchema,omitempty"`
	Result      any            `yaml:"result,omitempty"`
	Error       string         `yaml:"error,omitempty"`
	Responses   []Response     `yaml:"responses,omitempty"`
}

// Response is the answer of a mocked tool to the calls whose arguments include Args.
type Response struct {
	Args   map[string]any `yaml:"args"`
	Result any            `yaml:"result,omitempty"`
	Error  string         `yaml:"error,omitempty"`
}

// Expect is what a run must satisfy; every check set is one check of the run.
type Expect struct {
	// JSON: the answer is a JSON object.
	JSON bool `yaml:"json,omitempty"`
	// Items: the answer is {"items": [...]} and each of these is a subset of one of its items (maps compared key by
	// key, recursively; lists element by element).
	Items []map[string]any `yaml:"items,omitempty"`
	// Contains / NotContains: texts the answer holds or must not hold (case-insensitive).
	Contains    []string `yaml:"contains,omitempty"`
	NotContains []string `yaml:"notContains,omitempty"`
	// Trajectory: the tools called, compared with Match exact (the same calls in the same order), in_order (these
	// calls in this order, others between allowed) or any_order (these calls, in any order, others allowed).
	Trajectory *Trajectory `yaml:"trajectory,omitempty"`
	// MaxToolCalls bounds the calls (0: no bound); NoToolErrors fails a run where a call failed.
	MaxToolCalls int  `yaml:"maxToolCalls,omitempty"`
	NoToolErrors bool `yaml:"noToolErrors,omitempty"`
}

// Trajectory matches the tools called.
type Trajectory struct {
	Match string   `yaml:"match,omitempty"` // exact (default) | in_order | any_order
	Tools []string `yaml:"tools"`
}

// Judge scores the answer with a model against a rubric (0 to 1); the run fails under Threshold (default 0.7).
type Judge struct {
	Model     string  `yaml:"model,omitempty"`
	Rubric    string  `yaml:"rubric"`
	Threshold float64 `yaml:"threshold,omitempty"`
}

// Defaults.
const (
	DefaultMaxRounds = 6
	DefaultThreshold = 0.7
)

// Trajectory matches.
const (
	MatchExact    = "exact"
	MatchInOrder  = "in_order"
	MatchAnyOrder = "any_order"
)

// file is a case file: one case, or a list under "cases".
type file struct {
	Case  `yaml:",inline"`
	Cases []Case `yaml:"cases"`
}

// Load reads the cases of the files and directories given (*.yaml / *.yml, recursively), sorted by file then order.
func Load(paths ...string) ([]Case, error) {
	var files []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			files = append(files, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
				files = append(files, path)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	var out []Case
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		cs, err := Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for i := range cs {
			cs[i].File = f
		}
		out = append(out, cs...)
	}
	return out, nil
}

// Parse reads the cases of a YAML document and checks them.
func Parse(b []byte) ([]Case, error) {
	var f file
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	cases := f.Cases
	if f.Case.Name != "" || len(f.Case.Messages) > 0 {
		cases = append([]Case{f.Case}, cases...)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("no case")
	}
	seen := map[string]bool{}
	for i, c := range cases {
		if err := c.check(); err != nil {
			return nil, fmt.Errorf("case %d (%s): %w", i+1, c.Name, err)
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("case %q twice", c.Name)
		}
		seen[c.Name] = true
	}
	return cases, nil
}

func (c Case) check() error {
	switch {
	case c.Name == "":
		return fmt.Errorf("a case has a name")
	case len(c.Messages) == 0 || c.Messages[0].Role != "user":
		return fmt.Errorf("the messages start with a user message")
	case c.Prompt != "" && c.Prompt != "engine":
		return fmt.Errorf("prompt is \"engine\" or nothing")
	case c.MinPassRate < 0 || c.MinPassRate > 1:
		return fmt.Errorf("minPassRate is between 0 and 1")
	case c.Judge != nil && strings.TrimSpace(c.Judge.Rubric) == "":
		return fmt.Errorf("a judge has a rubric")
	}
	if t := c.Expect.Trajectory; t != nil && t.Match != "" && t.Match != MatchExact && t.Match != MatchInOrder && t.Match != MatchAnyOrder {
		return fmt.Errorf("trajectory match %q (exact, in_order or any_order)", t.Match)
	}
	names := map[string]bool{}
	for _, t := range c.Tools {
		if t.Name == "" || names[t.Name] {
			return fmt.Errorf("tool names are set and unique")
		}
		names[t.Name] = true
	}
	for _, m := range c.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			return fmt.Errorf("message role %q (user or assistant)", m.Role)
		}
	}
	return nil
}

func (c Case) messages() []llm.Message {
	out := make([]llm.Message, len(c.Messages))
	for i, m := range c.Messages {
		out[i] = llm.Message{Role: m.Role, Content: m.Content}
	}
	return out
}
