package main

import (
	"os"
	"strings"

	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/graph/importsvc"
	"github.com/zimwip/goap/lib/importerkit"
	"github.com/zimwip/goap/lib/jobkit"
	"github.com/zimwip/goap/platform/gitimport"
	"github.com/zimwip/goap/platform/jobsvc"
)

// jobService is the job service of this process (ADR 0149), its sweep following the process; its events go through
// its outbox to the event stream.
func jobService(e *env, st stores, rp *registryPart) *jobsvc.Service {
	jobs := &jobsvc.Service{Store: st.jobs, Log: e.log}
	e.emit(jobs.Emit(busSink(rp.bus)))
	go jobs.Run(e.ctx)
	return jobs
}

// jobWorker is the one worker of this process: the graph's kinds (exports, snapshot imports) and the git importer of
// the code domain (ADR 0148), writing to the graph of the process.
func jobWorker(e *env, gp *graphPart, jobs *jobsvc.Service, graphHandler *graphsvc.Handler) {
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
	kinds := graphHandler.JobRunners()
	for k, r := range importerkit.Runners(git) {
		kinds[k] = r
	}
	go (&jobkit.Worker{Queue: jobs, ID: jobkit.WorkerID("goap-dev"), Kinds: kinds, Log: e.log}).Run(e.ctx)
}

// importRegistry is the import registry of this process (ADR 0148): its runs are jobs of the job service.
func importRegistry(rp *registryPart, gp *graphPart, jobs *jobsvc.Service) *importsvc.Service {
	return &importsvc.Service{Domains: rp.domains, Authz: gp.authorizer, Jobs: jobs}
}
