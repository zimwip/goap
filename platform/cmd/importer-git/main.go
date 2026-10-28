// Command importer-git is the git importer service (ADR 0148): it implements the importer `git` the code domain declares
// (domains/code.yaml) and registers itself with the import registry of the graph service (see lib/importerkit), so the
// people run it from the platform (the History explorer, RPC goap.importer.v1.ImportService/StartImport). Each run is a
// gitimport.Importer writing through the graph service, one change per commit, as the person who started it.
//
// GOAP_GRAPH_URL is the graph service (also the registry, unless GOAP_IMPORT_URL says otherwise); GOAP_IMPORT_GIT_ROOTS
// (comma separated) are the local directories a run may read a repository from (none: only URLs, cloned into
// GOAP_IMPORT_GIT_CLONES, default <user cache>/goap/git). The symbols (Tree-sitter) need a build with cgo.
package main

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/importerkit"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/platform/gitimport"
)

func main() {
	var roots []string
	if v := os.Getenv("GOAP_IMPORT_GIT_ROOTS"); v != "" {
		roots = strings.Split(v, ",")
	}
	p := &gitimport.Plugin{
		Graph:    graphclient.NewClient(&http.Client{Timeout: 30 * time.Minute}, platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")),
		Roots:    roots,
		CloneDir: os.Getenv("GOAP_IMPORT_GIT_CLONES"),
	}
	if gitimport.TreeSitterAvailable {
		p.Symbols = func() (gitimport.Extractor, error) { return gitimport.NewTreeSitter() }
	}
	importerkit.Run("importer-git", p)
}
