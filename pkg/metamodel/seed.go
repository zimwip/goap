package metamodel

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/domains"
	"github.com/zimwip/goap/pkg/methodology"
)

// SeedMeta projects the platform domain onto the graph, from the copy embedded in the binary: the node types of the graph's own
// metadata (NodeType, LinkType, Lifecycle, Namespace) and of what the registry stores (versions of methodologies and domains, with the
// `version` lifecycle that gives their status). It is the bootstrap of the cycle "a methodology is a node that references node types,
// which are nodes": the first change is judged by a baseline that has no metadata, so it is accepted, and everything after is
// authored through changes like any node. It is idempotent.
func SeedMeta(ctx context.Context, g Graph) (Result, error) {
	data, err := domains.FS.ReadFile("platform.yaml")
	if err != nil {
		return Result{}, err
	}
	d, err := methodology.ParseDomain(data)
	if err != nil {
		return Result{}, fmt.Errorf("platform domain: %w", err)
	}
	return SyncDomain(ctx, g, d)
}
