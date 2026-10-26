package graphsvc

import (
	"context"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/access"
)

// openNamespaces are the namespaces every identified caller reads whole and the platform administers by its own rules
// (the built-in organisation, platform and methodology, ADR 0161 §4: their domains declare the scope public, which the
// reach set reads without looking at the versions): no project owns their reading or their writing.
var openNamespaces = map[string]bool{access.NamespaceOrganisation: true, domain.NamespacePlatform: true, domain.NamespaceMethodology: true}

// Reader is the principal a read is decided for (ADR 0161 §5): a platform service acting for a person (OnBehalfOf)
// reads as that person, so an agent, the assistant or a trigger run sees what the person sees; a person, or a service
// acting by itself, is itself.
func Reader(p authz.Principal) authz.Principal {
	if p.System() && p.OnBehalfOf != "" {
		return authz.Principal{Subject: p.OnBehalfOf}
	}
	return p
}

// ReachOf is graph.ReachOf over the reach set of a principal (access.Directory.ReachSet, ADR 0161 §5): a person, or
// a service acting for one, reads the versions whose scope reaches it and the open namespaces; a service acting by
// itself and an in-process call naming no principal (never an RPC: Handler.Identify refuses an anonymous request)
// read everything.
func ReachOf(reach func(ctx context.Context, p authz.Principal) (access.ReachSet, error)) graph.ReachOf {
	return func(ctx context.Context) (*graph.Reach, error) {
		p := Reader(authz.From(ctx))
		if p.System() || p.Anonymous() {
			return nil, nil
		}
		set, err := reach(ctx, p)
		if err != nil {
			return nil, err
		}
		keys := func(l []string) map[string]bool {
			m := make(map[string]bool, len(l))
			for _, k := range l {
				m[k] = true
			}
			return m
		}
		return &graph.Reach{Projects: keys(set.Projects), Trees: keys(set.Trees), Units: keys(set.Units), Subject: set.Subject,
			Namespaces: openNamespaces}, nil
	}
}

// OwnedWrites is graph.OwnedWrites (ADR 0161 §3): a change writes the nodes of a namespace only when its project owns
// them, but in the open namespaces, whose nodes the platform administers (the settings, the organisation).
func OwnedWrites(namespace string) bool { return !openNamespaces[namespace] }
