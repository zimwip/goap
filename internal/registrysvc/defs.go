package registrysvc

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"

	"github.com/zimwip/goap/pkg/methodology"
)

// A version of a methodology is a node (its header: the scalar fields, the status, the timestamps) and one node per
// element (condition, action, goal, agent, process), keyed "<header key>/<kind>/<name>" and typed by the built-in meta-domain
// methodology (methodology@Agent, ...; ADR 0023). The version is the graph content; the definition a caller gets is
// assembled from it.

// Kinds of the element nodes and the collection of the definition each one fills.
const (
	kindCondition = "condition"
	kindAction    = "action"
	kindGoal      = "goal"
	kindAgent     = "agent"
	kindProcess   = "process"
	kindMethod    = "method"
	kindRole      = "role"
	// kindStep and kindMethodStep are the steps of a process/method, materialized as their own nodes (one per
	// step and sub-step, architecture plan "Activity concept"): unlike the other kinds, they fill no top-level
	// collection field of their own (field ""), since they are authored inline within their owning process/method
	// element's own JSON (steps.go builds them from there) and ignored on decode (nothing round-trips through
	// them back into the Methodology struct) - they exist purely to give each step a real, versioned graph identity.
	kindStep       = "step"
	kindMethodStep = "methodStep"
)

// defKinds lists the element kinds with the JSON field of the collection they belong to.
var defKinds = []struct{ kind, field, nodeType string }{
	{kindCondition, "conditions", "methodology@Condition"},
	{kindAction, "actions", "methodology@Action"},
	{kindGoal, "goals", "methodology@Goal"},
	{kindAgent, "agents", "methodology@Agent"},
	{kindProcess, "processes", "methodology@Process"},
	{kindMethod, "methods", "methodology@Method"},
	{kindRole, "roles", "methodology@Role"},
	{kindStep, "", "methodology@Step"},
	{kindMethodStep, "", "methodology@MethodStep"},
}

// nodeTypeOf returns the graph node type of an element kind ("" when unknown).
func nodeTypeOf(kind string) string {
	for _, dk := range defKinds {
		if dk.kind == kind {
			return dk.nodeType
		}
	}
	return ""
}

// linkSubActivity ties an Activity (a process, method or step) to the sub-activities (steps) it is composed from.
const linkSubActivity = "methodology@sub_activity"

// defEl is an element of a definition.
type defEl struct {
	kind  string
	name  string
	props map[string]any // the element as JSON, with its position in the collection
}

func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

// split cuts a definition, given as JSON, into its header (what is not a collection) and its elements.
func split(raw map[string]any) (header map[string]any, els []defEl) {
	header = maps.Clone(raw)
	for _, k := range defKinds {
		list, _ := header[k.field].([]any)
		delete(header, k.field)
		seen := map[string]bool{}
		for i, item := range list {
			p, _ := item.(map[string]any)
			if p == nil {
				continue
			}
			name, _ := p["name"].(string)
			if seen[name] { // names that repeat are told apart by position
				name += "~" + strconv.Itoa(i)
			}
			seen[name] = true
			p["position"] = float64(i)
			els = append(els, defEl{kind: k.kind, name: name, props: p})
		}
	}
	return header, els
}

// join assembles the JSON of a definition from its header and elements (in position order).
func join(header map[string]any, els []map[string]any, kinds []string) map[string]any {
	raw := maps.Clone(header)
	byKind := map[string][]map[string]any{}
	for i, e := range els {
		byKind[kinds[i]] = append(byKind[kinds[i]], e)
	}
	for _, k := range defKinds {
		list := byKind[k.kind]
		if len(list) == 0 {
			continue
		}
		sort.SliceStable(list, func(i, j int) bool {
			a, _ := list[i]["position"].(float64)
			b, _ := list[j]["position"].(float64)
			return a < b
		})
		out := make([]any, len(list))
		for i, p := range list {
			p = maps.Clone(p)
			delete(p, "position")
			out[i] = p
		}
		raw[k.field] = out
	}
	return raw
}

func encodeMethodology(m methodology.Methodology) (map[string]any, []defEl, error) {
	raw, err := toMap(m)
	if err != nil {
		return nil, nil, err
	}
	header, els := split(raw)
	return header, els, nil
}

func decodeInto(header map[string]any, els []map[string]any, kinds []string, out any) error {
	raw := join(header, els, kinds)
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("definition: %w", err)
	}
	if m, ok := out.(*methodology.Methodology); ok {
		m.MigrateLegacyMethods() // stored before ADR 0050: methods named their agent
	}
	return nil
}
