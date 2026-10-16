package registrysvc

import "github.com/zimwip/goap/engine/methodology"

// The references between the elements of a version are links (ADR 0120, methodology.EncodeRefs): this is what the
// store adds to them: the steps' sub-activities and the capabilities of the steps.

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
}

func buildElementGraph(header map[string]any, els []defEl, subActivity map[string][]string) elementGraph {
	ix := indexElements(els)
	g := elementGraph{links: map[string][]methodology.RefLink{}}
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
	}
	return g
}
