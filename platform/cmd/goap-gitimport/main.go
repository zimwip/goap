// Command goap-gitimport loads a git repository into the graph, in the code domain (ADR 0146): one change per commit of
// the first-parent history, a node per directory and per file. It talks to the graph service (or goap-dev) and resumes
// where the last run stopped, so a long history (the Linux kernel) is imported in as many runs as wanted. With -symbols
// it reads the declarations of the source files with Tree-sitter, which needs a build with cgo.
//
//	goap-gitimport -repo ~/src/linux -name linux [-graph http://localhost:8080] [-project PROJ-ROOT] [-rev master] [-base v6.12] [-max 1000] [-symbols]
//
// It acts with the platform's identity headers (a system principal holding the admin role, the services behind the
// gateway), or through the gateway with -token.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/platform/gitimport"
)

var _ gitimport.Port = (*graphclient.Client)(nil)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("goap-gitimport", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var im gitimport.Importer
	graphURL := fs.String("graph", platform.Env("GOAP_GRAPH_URL", "http://localhost:8080"), "base URL of the graph service (or goap-dev)")
	token := fs.String("token", os.Getenv("GOAP_TOKEN"), "bearer token, to go through the gateway (default: identity headers of a system principal)")
	fs.StringVar(&im.Dir, "repo", "", "the git repository to import (a work tree or a bare repository)")
	fs.StringVar(&im.Name, "name", "", "name of the repository in the graph (default: the name of its directory)")
	fs.StringVar(&im.Source, "source", "", "where the repository comes from, recorded on its node (default: -repo)")
	fs.StringVar(&im.Rev, "rev", "HEAD", "revision followed: its first-parent history is imported")
	fs.StringVar(&im.Base, "base", "", "first import only: start at this revision, its tree as the first change (default: the root commit)")
	fs.IntVar(&im.Max, "max", 0, "import at most this many commits in this run (0: all)")
	fs.StringVar(&im.ProjectID, "project", "PROJ-ROOT", "project the changes act in")
	fs.StringVar(&im.OwnerOrg, "org", "", "unit holding the changes (default: the root unit)")
	fs.IntVar(&im.ProgressEvery, "progress", 100, "print a progress line every that many commits")
	symbols := fs.Bool("symbols", false, "read the declarations of the source files with Tree-sitter (functions, types, imports, variables...): a Symbol node each, defined in its file")
	fs.Int64Var(&im.MaxSymbolBytes, "symbols-max", 1<<20, "leave the source files larger than this many bytes out of -symbols")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if im.Dir == "" {
		fmt.Fprintln(stderr, "goap-gitimport: -repo is required")
		return 2
	}
	if im.Name == "" {
		abs, err := filepath.Abs(im.Dir)
		if err != nil {
			fmt.Fprintln(stderr, "goap-gitimport:", err)
			return 2
		}
		im.Name = strings.TrimSuffix(filepath.Base(abs), ".git")
	}
	if *symbols {
		ts, err := gitimport.NewTreeSitter()
		if err != nil {
			fmt.Fprintln(stderr, "goap-gitimport:", err)
			return 2
		}
		defer ts.Close()
		im.Symbols = ts
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hc := &http.Client{Timeout: 30 * time.Minute}
	if *token != "" {
		hc.Transport = bearer{token: *token, next: http.DefaultTransport}
	} else {
		ctx = authz.With(ctx, authz.System("gitimport", access.RoleAdmin))
	}
	im.Graph = graphclient.NewClient(hc, *graphURL)
	im.Progress = stdout
	st, err := im.Run(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "goap-gitimport:", err)
		if st.Last != "" {
			fmt.Fprintf(stderr, "goap-gitimport: imported up to %s, run again to resume\n", st.Last)
		}
		return 1
	}
	return 0
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}
