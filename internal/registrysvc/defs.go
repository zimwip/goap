package registrysvc

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"

	"github.com/zimwip/goap/pkg/methodology"
)

// A version of a methodology or a domain is a node (its header: the scalar fields, the status, the timestamps) and one node
// per element (condition, action, goal, agent, node type, link type, lifecycle, algorithm, algorithm instance), keyed
// "<header key>/<kind>/<name>" and typed "Def<Kind>". The version is the graph content; the definition a caller gets is
// assembled from it. These nodes are the authored definition; what the graph enforces at run time (NodeType, LinkType and
// Lifecycle nodes) is projected from the published version.

// Kinds of the element nodes and the collection of the definition each one fills.
const (
	kindCondition = "condition"
	kindAction    = "action"
	kindGoal      = "goal"
	kindAgent     = "agent"
	kindNodeType  = "nodetype"
	kindLinkType  = "linktype"
	kindLifecycle = "lifecycle"
	kindAlgorithm = "algorithm"
	kindInstance  = "instance"
)

// defKinds lists the element kinds with the JSON field of the collection they belong to.
var defKinds = []struct{ kind, field, nodeType string }{
	{kindCondition, "conditions", "DefCondition"},
	{kindAction, "actions", "DefAction"},
	{kindGoal, "goals", "DefGoal"},
	{kindAgent, "agents", "DefAgent"},
	{kindNodeType, "nodeTypes", "DefNodeType"},
	{kindLinkType, "linkTypes", "DefLinkType"},
	{kindLifecycle, "lifecycles", "DefLifecycle"},
	{kindAlgorithm, "algorithms", "DefAlgorithm"},
	{kindInstance, "algorithmInstances", "DefAlgorithmInstance"},
}

// DefTypes are the node types of the element nodes.
func DefTypes() []string {
	out := make([]string, len(defKinds))
	for i, k := range defKinds {
		out[i] = k.nodeType
	}
	return out
}

// schemaFields are the collections of a Schema (the others belong to the methodology itself).
var schemaFields = map[string]bool{"nodeTypes": true, "linkTypes": true, "lifecycles": true, "algorithms": true, "algorithmInstances": true}

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

// split cuts a definition, given as JSON, into its header (what is not a collection) and its elements. The
// collections of the schema sit at the top level (domain) or under "domain" (methodology).
func split(raw map[string]any) (header map[string]any, els []defEl) {
	header = maps.Clone(raw)
	collect := func(from map[string]any) {
		for _, k := range defKinds {
			list, _ := from[k.field].([]any)
			delete(from, k.field)
			seen := map[string]bool{}
			for i, item := range list {
				p, _ := item.(map[string]any)
				if p == nil {
					continue
				}
				name, _ := p["name"].(string)
				if seen[name] { // names that repeat (the same link type between several pairs) are told apart by position
					name += "~" + strconv.Itoa(i)
				}
				seen[name] = true
				p["position"] = float64(i)
				els = append(els, defEl{kind: k.kind, name: name, props: p})
			}
		}
	}
	collect(header)
	if sch, ok := header["domain"].(map[string]any); ok {
		collect(sch)
		if len(sch) == 0 {
			delete(header, "domain")
		}
	}
	return header, els
}

// join assembles the JSON of a definition from its header and elements (in position order).
// nested: the schema sits under "domain" (a methodology) instead of at the top level (a domain).
func join(header map[string]any, els []map[string]any, kinds []string, nested bool) map[string]any {
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
		if nested && schemaFields[k.field] {
			sch, _ := raw["domain"].(map[string]any)
			if sch == nil {
				sch = map[string]any{}
			}
			sch[k.field] = out
			raw["domain"] = sch
		} else {
			raw[k.field] = out
		}
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

func encodeDomain(d methodology.Domain) (map[string]any, []defEl, error) {
	raw, err := toMap(d)
	if err != nil {
		return nil, nil, err
	}
	header, els := split(raw)
	return header, els, nil
}

func decodeInto(header map[string]any, els []map[string]any, kinds []string, isMethodology bool, out any) error {
	raw := join(header, els, kinds, isMethodology)
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("definition: %w", err)
	}
	return nil
}
