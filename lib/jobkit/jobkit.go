// Package jobkit is the kit of the long runs (ADR 0149): the job service as its callers see it (Queue: the service in
// process, Client over RPC), the worker that claims and runs the jobs of its kinds (Worker), and the job as a runner
// sees it (Job: its parameters, its progress, its input, its output and its result).
//
// A service owning a kind authorizes a run and creates its job for the person who asked (Queue.Create); a worker,
// that service or another process, claims it, runs it and reports. The job service never interprets a kind.
package jobkit

import (
	"context"
	"errors"
	"google.golang.org/protobuf/types/known/timestamppb"
	"regexp"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	jobsv1 "github.com/zimwip/goap/contracts/gen/goap/jobs/v1"
)

// TokenHeader carries the services' token (GOAP_JOB_TOKEN) on their calls to the job service.
const TokenHeader = "X-Goap-Job-Token"

// Job statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Final says a status ends a job.
func Final(status string) bool {
	return status == StatusDone || status == StatusFailed || status == StatusCancelled
}

// DefaultLease is how long a claimed job holds without a report of its worker.
const DefaultLease = 30 * time.Second

// MaxWait bounds the wait of a claim.
const MaxWait = 30 * time.Second

// KindRE is the syntax of a kind: dot-separated lowercase words ("graph.export", "import.code.git").
var KindRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z0-9_-]+)*$`)

// Errors of the job service.
var (
	ErrNotFound  = errors.New("not found")
	ErrInvalid   = errors.New("invalid")
	ErrForbidden = authz.ErrForbidden
	// ErrLost: the job is no longer the worker's (its lease lapsed, or it ended).
	ErrLost = errors.New("job lost")
)

// Spec is a job to create.
type Spec struct {
	Kind, Label, Target string
	Params              map[string]any
	ProjectID, OwnerOrg string
	// By is the person the job runs for (required).
	By string
	// Principal is what the run acts as: the person's principal as the creating service saw it.
	Principal authz.Principal
	// Input is what the worker reads (Job.Input).
	Input []byte
}

// Claim is a job taken by a worker.
type Claim struct {
	Job       *jobsv1.Job
	Principal authz.Principal
	Lease     time.Duration
}

// Report is what a worker says of a running job; a final Status ends it.
type Report struct {
	Progress    string
	Done, Total int64
	Stats       map[string]int64
	Output      map[string]any
	Status      string
	Error       string
	// Log are the progress lines said since the last report.
	Log []LogLine
}

// MaxLog bounds the log of a job: the last lines of its feedback.
const MaxLog = 200

// LogLine is a line of the feedback of a run.
type LogLine struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// LogToPB and LogFromPB convert log lines to and from the protocol.
func LogToPB(lines []LogLine) []*jobsv1.LogLine {
	out := make([]*jobsv1.LogLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, &jobsv1.LogLine{At: timestamppb.New(l.At), Text: l.Text})
	}
	return out
}

func LogFromPB(lines []*jobsv1.LogLine) []LogLine {
	out := make([]LogLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, LogLine{At: l.GetAt().AsTime(), Text: l.GetText()})
	}
	return out
}

// Queue is the job service as the services and the workers call it.
type Queue interface {
	// Create queues a job.
	Create(ctx context.Context, s Spec) (*jobsv1.Job, error)
	// Job reads a job as the person of ctx: the service that created it for them follows it (a short job awaited).
	Job(ctx context.Context, id string) (*jobsv1.Job, error)
	// Workers lists the live workers and their kinds.
	Workers(ctx context.Context) ([]*jobsv1.Worker, error)
	// Claim takes the oldest queued job of the kinds, waiting up to wait; nil when none came.
	Claim(ctx context.Context, worker string, kinds []string, wait time.Duration) (*Claim, error)
	// Report records the progress of a running job of the worker and says whether it was cancelled.
	Report(ctx context.Context, id, worker string, r Report) (cancel bool, err error)
	// ReadInput reads the input of a job from offset; eof when nothing is left past what it returns.
	ReadInput(ctx context.Context, id, worker string, offset int64) (data []byte, eof bool, err error)
	// WriteResult appends a chunk to the result of a job (the first names it) and returns its size so far.
	WriteResult(ctx context.Context, id, worker, name, mediaType string, data []byte) (int64, error)
}

// LiveKinds are the kinds a live worker claims, with the first worker claiming each.
func LiveKinds(ctx context.Context, q Queue) (map[string]string, error) {
	ws, err := q.Workers(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, w := range ws {
		for _, k := range w.Kinds {
			if _, ok := out[k]; !ok {
				out[k] = w.Id
			}
		}
	}
	return out, nil
}
