package eval

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/pkg/llm"
)

const caseYAML = `
cases:
  - name: read-and-register
    prompt: engine
    system: Register the document you read.
    messages:
      - role: user
        content: Read spec.txt and register it.
    json: true
    runs: 2
    tools:
      - name: document-repository/read
        description: Read a document.
        inputSchema: {type: object, properties: {path: {type: string}}}
        error: not found
        responses:
          - args: {path: spec.txt}
            result: {text: the spec}
    expect:
      items:
        - {kind: artifact, type: doc, data: {seen: true}}
      trajectory: {match: in_order, tools: [document-repository/read]}
      maxToolCalls: 2
      noToolErrors: true
    judge:
      rubric: The agent registered the document it read.
  - name: plain
    messages: [{role: user, content: say hello}]
    expect: {contains: [hello], notContains: [goodbye]}
`

func TestParseChecksTheCases(t *testing.T) {
	cs, err := Parse([]byte(caseYAML))
	if err != nil || len(cs) != 2 || cs[0].Tools[0].Responses[0].Args["path"] != "spec.txt" || cs[0].Expect.Trajectory.Match != MatchInOrder {
		t.Fatalf("%+v %v", cs, err)
	}
	for doc, want := range map[string]string{
		`messages: [{role: user, content: x}]`:                                                                      "a case has a name",
		`{name: a, messages: [{role: assistant, content: x}]}`:                                                      "start with a user message",
		`{name: a, prompt: other, messages: [{role: user, content: x}]}`:                                            "prompt",
		`{name: a, messages: [{role: user, content: x}], judge: {rubric: " "}}`:                                     "rubric",
		`{name: a, messages: [{role: user, content: x}], expect: {trajectory: {match: some, tools: []}}}`:           "trajectory match",
		`cases: [{name: a, messages: [{role: user, content: x}]}, {name: a, messages: [{role: user, content: y}]}]`: "twice",
	} {
		if _, err := Parse([]byte(doc)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", doc, err, want)
		}
	}
}

func TestRunScoresTheAnswerTheTrajectoryAndTheJudge(t *testing.T) {
	cs, _ := Parse([]byte(caseYAML))
	var mu sync.Mutex
	var reqs []llm.Request
	client := llm.ClientFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, r)
		if llm.MetaFrom(ctx).Source != llm.SourceEval {
			t.Errorf("source %q", llm.MetaFrom(ctx).Source)
		}
		switch {
		case strings.Contains(r.System, "You evaluate the answer"):
			return llm.Response{Text: `{"score": 0.9, "reason": "it registered it"}`, Usage: llm.Usage{InputTokens: 5}}, nil
		case len(r.Messages) == 1:
			return llm.Response{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "document-repository/read", Arguments: map[string]any{"path": "spec.txt"}}},
				Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}}, nil
		}
		return llm.Response{Text: `{"items":[{"kind":"artifact","type":"doc","data":{"seen":true,"extra":1}}]}`, Usage: llm.Usage{InputTokens: 120, OutputTokens: 20}}, nil
	})
	rep := Runner{Client: client}.Run(context.Background(), cs[0])
	if !rep.Passed || rep.PassRate != 1 || len(rep.Runs) != 2 || rep.ToolCalls != 1 {
		t.Fatalf("report %+v", rep)
	}
	run := rep.Runs[0]
	if run.Score == nil || *run.Score != 0.9 || run.InputTokens != 225 || run.Rounds != 1 {
		t.Fatalf("run %+v", run)
	}
	// the engine's system text comes first, the tools are given natively, the mocked answer goes back by call id
	first, second := reqs[0], reqs[1]
	if !strings.HasPrefix(first.System, "You are an agent of an enterprise methodology platform") || !strings.HasSuffix(first.System, "Register the document you read.") ||
		len(first.Tools) != 1 || !first.JSON {
		t.Fatalf("first request %+v", first)
	}
	tr := second.Messages[2].ToolResults
	if len(tr) != 1 || tr[0].CallID != "c1" || tr[0].IsError || tr[0].Content != `{"text":"the spec"}` {
		t.Fatalf("tool results %+v", tr)
	}
}

func TestFailedChecksFailTheRun(t *testing.T) {
	cs, _ := Parse([]byte(caseYAML))
	c := cs[0]
	c.Runs, c.Judge = 1, nil
	// the model reads another path (the mocked error answers), then answers without the item
	n := 0
	client := llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		n++
		if n == 1 {
			return llm.Response{Text: `{"tool_calls":[{"name":"document-repository/read","arguments":{"path":"other"}},{"name":"ticketing/create","arguments":{}}]}`}, nil
		}
		return llm.Response{Text: `{"items":[]}`}, nil
	})
	rep := Runner{Client: client}.Run(context.Background(), c)
	run := rep.Runs[0]
	failed := map[string]bool{}
	for _, ch := range run.Checks {
		if !ch.Passed {
			failed[ch.Name] = true
		}
	}
	if rep.Passed || !failed["item 1"] || !failed["no tool errors"] || failed["trajectory in_order"] || run.ToolErrors != 2 || run.Rounds != 1 {
		t.Fatalf("checks %+v errors %d", run.Checks, run.ToolErrors)
	}
	// an exact trajectory fails on the extra call, and so does the bound
	c.Expect.Trajectory.Match, c.Expect.MaxToolCalls = MatchExact, 1
	n = 0
	run = Runner{Client: client}.Run(context.Background(), c).Runs[0]
	if run.Checks[2].Name != "trajectory exact" || run.Checks[2].Passed || run.Checks[3].Passed {
		t.Fatalf("checks %+v", run.Checks)
	}
}

func TestPassRateAndModelOverride(t *testing.T) {
	cs, _ := Parse([]byte(caseYAML))
	c := cs[1]
	c.Runs, c.MinPassRate = 4, 0.5
	n := 0
	var models []string
	client := llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		n++
		models = append(models, r.Model)
		if n%2 == 0 {
			return llm.Response{Text: "goodbye"}, nil
		}
		return llm.Response{Text: "Hello there"}, nil
	})
	rep := Runner{Client: client, Model: "fast"}.RunAll(context.Background(), []Case{c})
	if rep.Passed != 1 || rep.Cases[0].PassRate != 0.5 || models[0] != "fast" || rep.Cases[0].Model != "fast" {
		t.Fatalf("report %+v models %v", rep, models)
	}
}

func TestTrajectoryMatches(t *testing.T) {
	got := []string{"a", "x", "b", "a"}
	for _, tc := range []struct {
		match string
		tools []string
		want  bool
	}{
		{MatchExact, []string{"a", "x", "b", "a"}, true},
		{MatchExact, []string{"a", "b"}, false},
		{MatchInOrder, []string{"a", "b"}, true},
		{MatchInOrder, []string{"b", "x"}, false},
		{MatchAnyOrder, []string{"b", "a", "a"}, true},
		{MatchAnyOrder, []string{"a", "a", "a"}, false},
	} {
		if trajectoryMatches(&Trajectory{Match: tc.match, Tools: tc.tools}, got) != tc.want {
			t.Errorf("%s %v: want %v", tc.match, tc.tools, tc.want)
		}
	}
}

func TestSubset(t *testing.T) {
	got := map[string]any{"kind": "artifact", "data": map[string]any{"n": 3.0, "tags": []any{"a", "b"}}}
	for want, ok := range map[string]bool{
		`{"kind": "artifact"}`:      true,
		`{"data": {"n": 3}}`:        true,
		`{"data": {"tags": ["b"]}}`: true,
		`{"data": {"tags": ["c"]}}`: false,
		`{"kind": "decision"}`:      false,
		`{"missing": "x"}`:          false,
	} {
		var w map[string]any
		cs, _ := Parse([]byte("{name: a, messages: [{role: user, content: x}], expect: {items: [" + want + "]}}"))
		w = cs[0].Expect.Items[0]
		if subset(w, got) != ok {
			t.Errorf("%s: want %v", want, ok)
		}
	}
}
