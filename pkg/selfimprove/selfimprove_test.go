package selfimprove

import (
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/observe"
)

// A proposal names the elements of the methodology; written on the graph, the names are links and the links of the
// fields it sets replace those of the node (ADR 0120).
func TestGraphWriteTurnsNamesIntoLinks(t *testing.T) {
	m := &methodology.Methodology{Name: "m", Version: "1", Actions: []methodology.Action{{Name: "a"}, {Name: "b"}, {Name: "c"}}, Agents: []methodology.Agent{{Name: "ag"}}}
	hkey := observe.ElementKey("m", "1", "methodology", "")
	uses := func(id, to string, pos float64) storedLink {
		return storedLink{domain.Link{ID: domain.LinkID(id), Type: methodology.LinkUses, Properties: map[string]any{"position": pos}}, hkey + "/action/" + to}
	}
	stored := []storedLink{uses("L1", "a", 0), uses("L2", "b", 1), uses("L3", "c", 2),
		{domain.Link{ID: "L4", Type: methodology.LinkPursues, Properties: map[string]any{"position": float64(0)}}, hkey + "/goal/g"}}
	w := graphWrite(observe.Proposal{Op: "update_node", Key: hkey + "/agent/ag", Props: map[string]any{"actions": []any{"a", "c"}}}, methodology.RefIndexOf(m), hkey, stored)
	if !slices.Equal(w.RemoveLinks, []string{"L2", "L3"}) || len(w.Links) != 1 || w.Links[0].To != hkey+"/action/c" || w.Links[0].Props["position"] != float64(1) {
		t.Fatalf("links of the write: remove %v, add %+v", w.RemoveLinks, w.Links)
	}
	if v, ok := w.Props["actions"]; !ok || v != nil {
		t.Fatalf("the names are links, the property is cleared: %+v", w.Props)
	}
}
