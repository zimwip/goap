package main

import (
	"os"
	"strings"

	"github.com/zimwip/goap/graph/importsvc"
	"github.com/zimwip/goap/lib/importerkit"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/platform/gitimport"
)

// importRegistry is the import registry of this process (ADR 0148) with the git importer of the code domain running in
// it, writing to the graph of the process (no token: no importer service of another process registers).
func importRegistry(e *env, rp *registryPart, gp *graphPart) *importsvc.Service {
	reg := &importsvc.Service{Domains: rp.domains, Authz: gp.authorizer, HTTPClient: platform.H2CClient()}
	roots := []string{}
	if e.cfg.ImportGitRoots != "" {
		roots = strings.Split(e.cfg.ImportGitRoots, ",")
	} else if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, home)
	}
	git := &gitimport.Plugin{Graph: gp.g, Roots: roots}
	if gitimport.TreeSitterAvailable {
		git.Symbols = func() (gitimport.Extractor, error) { return gitimport.NewTreeSitter() }
	}
	svc := importerkit.New(git, "")
	svc.Log = e.log
	reg.AddLocal(svc)
	return reg
}
