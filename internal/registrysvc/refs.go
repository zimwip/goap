package registrysvc

import (
	"fmt"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/methodology"
)

// The references between the elements of a version are links (ADR 0119, methodology.EncodeRefs): this is what the
// store adds to them, the steps' sub-activities, the capabilities of the steps, and the rule that no element stands
// alone.

// sameRef reports whether a stored link is the reference wanted (type, target and attributes).
func sameRef(w methodology.RefLink, typ, to string, props map[string]any) bool {
	return w.Type == typ && w.To == to && jsonEqual(normProps(w.Props), normProps(props))
}

func normProps(p map[string]any) map[string]any {
	if len(p) == 0 {
		return nil
	}
	return p
}

// indexElements indexes the elements of a definition by kind and name (the first one when names repeat).
func indexElements(els []defEl) methodology.RefIndex {
	ix := methodology.RefIndex{}
	for _, e := range els {
		if name, _ := e.props["name"].(string); name != "" {
			ix.Add(e.kind, name, e.kind+"/"+e.name)
		}
	}
	return ix
}

// capabilityLinks are the carried_out_by links of the steps naming a capability (`method`): to every method providing
// it (`for`). They are derived from the definition (the capability is a name, not an element) and never read back.
func capabilityLinks(els []defEl) map[string][]methodology.RefLink {
	providers := map[string][]string{}
	for _, e := range els {
		if e.kind != kindMethod {
			continue
		}
		if capability, _ := e.props["for"].(string); capability != "" {
			providers[capability] = append(providers[capability], e.kind+"/"+e.name)
		}
	}
	out := map[string][]methodology.RefLink{}
	for _, e := range els {
		if e.kind != kindStep && e.kind != kindMethodStep {
			continue
		}
		capability, _ := e.props["method"].(string)
		for _, rel := range providers[capability] {
			out[e.kind+"/"+e.name] = append(out[e.kind+"/"+e.name], methodology.RefLink{Type: methodology.LinkCarriedOutBy, To: rel})
		}
	}
	return out
}

// elementGraph is a definition as the graph holds it: the properties of the header and of every element with their
// references cut out, the links of each element by relative key (references, sub-activities, capabilities) and those
// of the header.
type elementGraph struct {
	header      map[string]any
	headerLinks []methodology.RefLink
	els         []defEl
	links       map[string][]methodology.RefLink // relative key -> outgoing links
	outside     map[string]bool                  // relative key -> it names something outside the version
	implicit    map[string]bool                  // relative key -> an agent naming no action or no goal: it has them all
}

func buildElementGraph(header map[string]any, els []defEl, subActivity map[string][]string) elementGraph {
	ix := indexElements(els)
	g := elementGraph{links: map[string][]methodology.RefLink{}, outside: map[string]bool{}, implicit: map[string]bool{}}
	h := methodology.EncodeRefs(methodology.ElementVersion, header, ix)
	g.header, g.headerLinks = h.Props, append(h.Links, methodology.DerivedRefs(methodology.ElementVersion, h.Props, ix, "")...)
	for rel, ls := range capabilityLinks(els) {
		g.links[rel] = append(g.links[rel], ls...)
	}
	for _, e := range els {
		rel := e.kind + "/" + e.name
		enc := methodology.EncodeRefs(e.kind, e.props, ix)
		g.els = append(g.els, defEl{kind: e.kind, name: e.name, props: enc.Props})
		g.links[rel] = append(append(enc.Links, g.links[rel]...), methodology.DerivedRefs(e.kind, enc.Props, ix, rel)...)
		for _, child := range subActivity[rel] {
			g.links[rel] = append(g.links[rel], methodology.RefLink{Type: linkSubActivity, To: child})
		}
		g.outside[rel] = enc.Outside
		if e.kind == kindAgent {
			actions, _ := e.props["actions"].([]any)
			goals, _ := e.props["goals"].([]any)
			g.implicit[rel] = len(actions) == 0 || len(goals) == 0
		}
	}
	return g
}

// isolated lists the elements connected to no other element of the version (ADR 0119): no link of theirs reaches
// another element, none reaches them, and they name nothing outside the version. The header defines every element and
// does not count. An agent naming no action (or no goal) plans over all of them: it is not isolated.
func (g elementGraph) isolated() []defEl {
	degree := map[string]int{}
	for from, ls := range g.links {
		for _, l := range ls {
			if l.To != from {
				degree[from]++
				degree[l.To]++
			}
		}
	}
	for _, l := range g.headerLinks {
		degree[l.To]++
	}
	var out []defEl
	for _, e := range g.els {
		rel := e.kind + "/" + e.name
		if degree[rel] > 0 || g.outside[rel] || g.implicit[rel] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// connectivityIssue says an element is isolated, at the path of its name in the definition.
func connectivityIssue(e defEl) (path, msg string) {
	field := ""
	for _, k := range defKinds {
		if k.kind == e.kind {
			field = k.field
		}
	}
	pos, _ := e.props["position"].(float64)
	name, _ := e.props["name"].(string)
	noun := strings.ToLower(nodeTypeOf(e.kind)[len("methodology@"):])
	return fmt.Sprintf("%s[%d].name", field, int(pos)), fmt.Sprintf("%s %s is connected to nothing: no element of the methodology refers to it and it refers to none (a condition is the input or the output of an activity or a goal, a role is assigned, an action is run by a step, an agent or a method); use it or remove it", noun, name)
}

// connectivityIssues reports the elements of a definition connected to nothing (ADR 0119): by default no element of a
// methodology stands alone, it is linked to the rest of the version.
func connectivityIssues(m methodology.Methodology) def.Issues {
	eg, err := encodeVersion(m, "", time.Time{}, time.Time{}, time.Time{}, "")
	if err != nil {
		return nil
	}
	var out def.Issues
	for _, e := range eg.isolated() {
		path, msg := connectivityIssue(e)
		out = append(out, def.Issue{Path: path, Message: msg})
	}
	return out
}
