// Package importerkit is the toolkit to write an importer service (ADR 0148): a separately deployed service that loads
// a source into the graph (a git repository, a spreadsheet, another tool) for an importer a domain declares.
//
// An importer implements Importer and calls Run from its main:
//
//	func main() { importerkit.Run("importer-git", gitimport.Plugin{...}) }
//
// A run is a job (ADR 0149): the import registry (served by the graph service) checks it and creates the job of kind
// import.<domain>.<importer> for the person who asked; the service is a worker of the job service claiming the jobs of
// the importers it implements (the registry tells from the live workers which importers are available). The service
// listens on nothing: it reaches the job service and the graph.
//
// Environment: GOAP_JOBS_URL (the job service; default http://localhost:8091), GOAP_JOB_TOKEN (the services' token of
// the job service, required by it).
package importerkit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zimwip/goap/contracts/authz"
	importerv1 "github.com/zimwip/goap/contracts/gen/goap/importer/v1"
	"github.com/zimwip/goap/lib/jobkit"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
)

// KindPrefix starts the kinds of the import jobs.
const KindPrefix = "import."

// Kind is the job kind of an importer of a domain: import.<domain>.<importer>.
func Kind(domain, importer string) string { return KindPrefix + domain + "." + importer }

// SplitKind reads Kind back (ok false for another kind).
func SplitKind(kind string) (domain, importer string, ok bool) {
	rest, found := strings.CutPrefix(kind, KindPrefix)
	if !found {
		return "", "", false
	}
	domain, importer, ok = strings.Cut(rest, ".")
	return domain, importer, ok && domain != "" && importer != "" && !strings.Contains(importer, ".")
}

// Job is a run of an importer, as the importer sees it: its parameters (String, Int, Bool), the project and unit its
// changes act in, the person it runs for (By), and its progress (Progress, Set; an io.Writer of progress lines).
type Job = jobkit.Job

// Importer loads a source into the graph.
type Importer interface {
	// Info describes the service and the importers it implements (domain and importer names, as the domains declare
	// them).
	Info() *importerv1.ImporterInfo
	// Run runs one import and returns when it is over. ctx carries the principal the run acts as (a system principal of
	// the service holding the admin role, on behalf of the person who started it: the registry authorized the run) and
	// is cancelled when the job is; job carries the parameters and takes the progress.
	Run(ctx context.Context, job *Job) error
}

// BrowsePrefix starts the kinds of the short jobs a folder chooser awaits.
const BrowsePrefix = "browse."

// BrowseKind is the job kind of the chooser of an importer of a domain: browse.<domain>.<importer>.
func BrowseKind(domain, importer string) string { return BrowsePrefix + domain + "." + importer }

// Browser is an importer that lists what a parameter may name as it reads it (the directories under its roots, for a
// parameter whose widget is "directory"): the folder chooser of the web, answered as a short job. path empty: the
// roots.
type Browser interface {
	Browse(ctx context.Context, domain, importer, param, path string) (*importerv1.BrowseImportResponse, error)
}

// Runners are the job runners of an importer: one kind per importer it implements, each running as the service on
// behalf of the person, and the kind of its chooser when it is a Browser (the listing is the output of the job, under
// the key "listing").
func Runners(im Importer) map[string]jobkit.Runner {
	info := im.Info()
	out := map[string]jobkit.Runner{}
	for _, x := range info.GetImplements() {
		out[Kind(x.Domain, x.Importer)] = func(ctx context.Context, j *jobkit.Job) error {
			return im.Run(authz.With(ctx, authz.System(info.GetId(), "admin").For(j.By)), j)
		}
		b, ok := im.(Browser)
		if !ok {
			continue
		}
		out[BrowseKind(x.Domain, x.Importer)] = func(ctx context.Context, j *jobkit.Job) error {
			l, err := b.Browse(authz.With(ctx, authz.System(info.GetId(), "admin").For(j.By)), x.Domain, x.Importer, j.String("param"), j.String("path"))
			if err != nil {
				return err
			}
			v, err := ListingValue(l)
			if err != nil {
				return err
			}
			j.Output(map[string]any{"listing": v})
			return nil
		}
	}
	return out
}

// ListingValue is a listing as the output of a job holds it.
func ListingValue(l *importerv1.BrowseImportResponse) (map[string]any, error) {
	b, err := protojson.Marshal(l)
	if err != nil {
		return nil, err
	}
	var v map[string]any
	return v, json.Unmarshal(b, &v)
}

// ListingOf reads ListingValue back from the output of a job.
func ListingOf(output map[string]any) (*importerv1.BrowseImportResponse, error) {
	l := &importerv1.BrowseImportResponse{}
	v, ok := output["listing"]
	if !ok {
		return nil, errors.New("the job answered no listing")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return l, protojson.Unmarshal(b, l)
}

// Worker is the worker of an importer over a job queue.
func Worker(q jobkit.Queue, im Importer) *jobkit.Worker {
	return &jobkit.Worker{Queue: q, ID: jobkit.WorkerID(im.Info().GetId()), Kinds: Runners(im)}
}

// Run runs the importer as a worker of the job service until the process is stopped.
func Run(name string, im Importer) {
	log := platform.Logger(name)
	defer telemetry.Setup(context.Background(), log, name)(context.Background())
	token := os.Getenv("GOAP_JOB_TOKEN")
	if token == "" {
		log.Warn("GOAP_JOB_TOKEN is not set: the job service refuses the claims")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	w := Worker(jobkit.NewClient(platform.H2CClient(), platform.Env("GOAP_JOBS_URL", "http://localhost:8091"), token, telemetry.ClientOptions()...), im)
	w.Log = log
	log.Info("importer started", "id", im.Info().GetId(), "kinds", len(w.Kinds))
	w.Run(ctx)
}
