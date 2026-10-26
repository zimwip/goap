package registrysvc

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/engine/methodology"
)

// rustMethodologyCases is where the Rust graph reads the shipped methodologies as the store holds them, the definition
// it must read back from their nodes and the conditions it must derive from that definition (ADR 0163 addendum, what a
// change follows).
const rustMethodologyCases = "../../rust/crates/graph-core/src/conditions/methodology_cases.gen.json"

type rustNode struct {
	Key   string         `json:"key"`
	Type  string         `json:"type"`
	Props map[string]any `json:"props"`
}

type rustLink struct {
	From  string         `json:"from"`
	Type  string         `json:"type"`
	To    string         `json:"to"`
	Props map[string]any `json:"props,omitempty"`
}

type rustMethodology struct {
	File       string            `json:"file"`
	Nodes      []rustNode        `json:"nodes"`
	Links      []rustLink        `json:"links"`
	Definition map[string]any    `json:"definition"`
	Conditions map[string]string `json:"conditions"`
	// Evaluations are the values of the conditions on the activations of the Rust activation cases.
	Evaluations []rustEvaluation `json:"evaluations"`
	// StepDeps are the steps each step relies on (Compiled.StepDependencies), what the history of a change is folded
	// with (ADR 0109 §5).
	StepDeps map[string][]string `json:"stepDeps,omitempty"`
}

type rustEvaluation struct {
	Board    string                         `json:"board"`
	State    map[string]bool                `json:"state"`
	Errors   []string                       `json:"errors,omitempty"`
	Subjects map[string][]condition.Subject `json:"subjects,omitempty"`
}

// rustActivations are the activations of the Rust activation cases (engine/condition), read back as Go builds them:
// an integer an int64, every other number a float64.
func rustActivations(t *testing.T) []struct {
	name string
	act  func() map[string]any
} {
	raw, err := os.ReadFile("../../rust/crates/graph-core/src/conditions/activation_cases.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Name       string         `json:"name"`
			Activation map[string]any `json:"activation"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	var untyped func(v any) any
	untyped = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			if n, ok := x["#int"]; ok && len(x) == 1 {
				return int64(n.(float64))
			}
			out := map[string]any{}
			for k, e := range x {
				out[k] = untyped(e)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = untyped(e)
			}
			return out
		}
		return v
	}
	var out []struct {
		name string
		act  func() map[string]any
	}
	for _, c := range file.Cases {
		a := c.Activation
		out = append(out, struct {
			name string
			act  func() map[string]any
		}{c.Name, func() map[string]any { return untyped(a).(map[string]any) }})
	}
	return out
}

// TestRustMethodologyCasesAreCurrent writes (GOAP_UPDATE_RUST=1) or checks the methodology cases of the Rust graph.
func TestRustMethodologyCasesAreCurrent(t *testing.T) {
	files, _ := filepath.Glob("../../methodologies/*.yaml")
	examples, _ := filepath.Glob("../../methodologies/examples/*.yaml")
	files = append(files, examples...)
	sort.Strings(files)
	var cases []rustMethodology
	boards := rustActivations(t)
	for _, f := range files {
		m, err := methodology.LoadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		c, err := m.Compile()
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		eg, err := encodeVersion(*m, StatusPublished, at, at, at, "")
		if err != nil {
			t.Fatal(err)
		}
		cs := storedCase(MethodologyVersionKey(m.Name, m.Version), eg)
		cs.File = strings.TrimPrefix(f, "../../")
		cs.Conditions = c.Conditions.Exprs()
		for later, on := range c.StepDependencies() {
			if len(on) == 0 {
				continue
			}
			if cs.StepDeps == nil {
				cs.StepDeps = map[string][]string{}
			}
			cs.StepDeps[later] = slices.Sorted(maps.Keys(on))
		}
		for _, b := range boards {
			res := c.Conditions.EvaluateActivation(b.act())
			ev := rustEvaluation{Board: b.name, State: res.State, Subjects: res.Subjects}
			for _, k := range slices.Sorted(maps.Keys(res.Errors)) {
				ev.Errors = append(ev.Errors, k)
			}
			cs.Evaluations = append(cs.Evaluations, ev)
		}
		cs.Definition, err = readBack(cs)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		cases = append(cases, cs)
	}
	got, err := json.MarshalIndent(map[string]any{"cases": cases}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if os.Getenv("GOAP_UPDATE_RUST") == "1" {
		if err := os.WriteFile(rustMethodologyCases, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	have, err := os.ReadFile(rustMethodologyCases)
	if err != nil || string(have) != string(got) {
		t.Fatalf("%s is not current: run GOAP_UPDATE_RUST=1 go test ./platform/registrysvc/ -run TestRustMethodologyCasesAreCurrent", rustMethodologyCases)
	}
}

// storedCase lays a version out as the store writes it: the header, its elements and the links of each.
func storedCase(hkey string, eg elementGraph) rustMethodology {
	cs := rustMethodology{Nodes: []rustNode{{Key: hkey, Type: TypeMethodologyVersion, Props: eg.header}}}
	for _, l := range eg.headerLinks {
		cs.Links = append(cs.Links, rustLink{hkey, l.Type, hkey + "/" + l.To, normProps(l.Props)})
	}
	for _, e := range eg.els {
		rel := e.kind + "/" + e.name
		k := hkey + "/" + rel
		cs.Nodes = append(cs.Nodes, rustNode{Key: k, Type: nodeTypeOf(e.kind), Props: e.props})
		for _, l := range eg.links[rel] {
			cs.Links = append(cs.Links, rustLink{k, l.Type, hkey + "/" + l.To, normProps(l.Props)})
		}
		cs.Links = append(cs.Links, rustLink{From: hkey, Type: linkDefines, To: k})
	}
	return cs
}

// readBack is the definition the store reads from the nodes of a case (versionJSON), through JSON as the Rust graph
// reads it.
func readBack(cs rustMethodology) (map[string]any, error) {
	raw, err := json.Marshal(cs)
	if err != nil {
		return nil, err
	}
	var c rustMethodology
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	byKey := map[string]domain.Node{}
	byID := map[domain.NodeID]domain.Node{}
	elements := map[string]domain.Node{}
	var header domain.Node
	for _, n := range c.Nodes {
		node := domain.Node{ID: domain.NodeID(n.Key), Version: 1, Key: n.Key, Type: n.Type, Namespace: NamespaceMethodology, Properties: n.Props}
		byKey[n.Key], byID[node.ID] = node, node
		if n.Type == TypeMethodologyVersion {
			header = node
		} else {
			elements[n.Key] = node
		}
	}
	out := map[domain.NodeID][]keyedLink{}
	for i, l := range c.Links {
		to, ok := byKey[l.To]
		if !ok {
			return nil, fmt.Errorf("link to %s: no node", l.To)
		}
		link := domain.Link{ID: domain.LinkID(fmt.Sprintf("l%06d", i)), Type: l.Type, From: domain.NodeRef{ID: domain.NodeID(l.From), Version: 1},
			To: domain.NodeRef{ID: to.ID, Version: 1}, Properties: l.Props}
		out[link.From.ID] = append(out[link.From.ID], keyedLink{link, to.Key})
	}
	var st Status
	var created, updated, published time.Time
	var by string
	def, err := versionJSON(header, elements, out, byID, &st, &created, &updated, &published, &by)
	if err != nil {
		return nil, err
	}
	if st == "" {
		return nil, fmt.Errorf("no status")
	}
	return def, nil
}
