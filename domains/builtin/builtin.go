// Package builtin embeds the domains shipped with the platform (ADR 0012 §4): the meta-domains methodology and
// domain, and the frozen organisation and platform domains. The YAML files are the source; pkg/methodology parses them.
package builtin

import "embed"

// FS holds the YAML definitions of the built-in domains.
//
//go:embed *.yaml
var FS embed.FS
