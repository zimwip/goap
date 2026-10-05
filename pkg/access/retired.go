package access

import "github.com/zimwip/goap/pkg/domain"

// StateRetired is the state a node with no parent is taken out of force in (ADR 0076 §4c: lifecycle config of the
// built-in domains, transition retire / restore): it is never deleted, its readers leave it out.
const StateRetired = "retired"

// InForce leaves out the nodes their lifecycle retired, and the links from or to them.
func InForce(nodes []domain.Node, links []domain.Link) ([]domain.Node, []domain.Link) {
	retired := map[domain.NodeID]bool{}
	out := make([]domain.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.State == StateRetired {
			retired[n.ID] = true
			continue
		}
		out = append(out, n)
	}
	if len(retired) == 0 {
		return nodes, links
	}
	kept := make([]domain.Link, 0, len(links))
	for _, l := range links {
		if !retired[l.From.ID] && !retired[l.To.ID] {
			kept = append(kept, l)
		}
	}
	return out, kept
}
