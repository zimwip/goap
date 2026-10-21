package graphsvc

import (
	"github.com/zimwip/goap/contracts/lnk"
	"github.com/zimwip/goap/lib/graphclient"
)

// Resolvers are the resolvers of the kinds of source the graph service reads beyond its own namespaces (ADR 0154): a
// namespace of another graph service (lnk.KindGraph) and a directory of files (lnk.KindFiles). A source is declared by
// a platform@Source node, written by administrators only (adminOnly).
func Resolvers() map[string]lnk.Resolver {
	return map[string]lnk.Resolver{
		lnk.KindGraph: &graphclient.Federation{},
		lnk.KindFiles: lnk.Files{},
	}
}
