// Package domains embeds the shared domains of the platform, so that a service can seed the graph with the ones it needs
// before anything else (the platform domain describes the graph's own metadata).
package domains

import "embed"

// FS holds the domain files.
//
//go:embed *.yaml
var FS embed.FS
