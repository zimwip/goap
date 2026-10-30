package jobkit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/zimwip/goap/contracts/form"
	jobsv1 "github.com/zimwip/goap/contracts/gen/goap/jobs/v1"
	"github.com/zimwip/goap/lib/pbconv"
)

// Request is a question a running job asks the person it runs for, and waits for (ADR 0149 addendum): a form the job
// defines (the form of a human action, contracts/form, ADR 0115), answered with its values. A job has one request at a
// time; the job keeps the last one, answered or not.
type Request struct {
	ID      string    `json:"id"`
	Form    form.Form `json:"form"`
	AskedAt time.Time `json:"askedAt"`
	// Answer are the values the person gave, checked against the form; AnsweredAt is zero while the job waits.
	Answer     map[string]any `json:"answer,omitempty"`
	AnsweredAt time.Time      `json:"answeredAt,omitzero"`
}

// Pending says the job waits for the answer to the request.
func (r *Request) Pending() bool { return r != nil && r.ID != "" && r.AnsweredAt.IsZero() }

// Clone copies a request (nil for none).
func (r *Request) Clone() *Request {
	if r == nil {
		return nil
	}
	c := *r
	c.Answer = maps.Clone(r.Answer)
	return &c
}

// RequestToPB and RequestFromPB convert a request to and from the protocol (nil for none).
func RequestToPB(r *Request) *jobsv1.JobRequest {
	if r == nil || r.ID == "" {
		return nil
	}
	return &jobsv1.JobRequest{Id: r.ID, Form: pbconv.FormToPB(&r.Form), AskedAt: pbconv.Time(r.AskedAt), Answer: pbconv.Struct(r.Answer),
		AnsweredAt: pbconv.Time(r.AnsweredAt)}
}

func RequestFromPB(p *jobsv1.JobRequest) *Request {
	if p == nil || p.Id == "" {
		return nil
	}
	r := &Request{ID: p.Id, AskedAt: pbconv.FromTime(p.AskedAt), Answer: pbconv.Map(p.Answer), AnsweredAt: pbconv.FromTime(p.AnsweredAt)}
	if f := pbconv.FormFromPB(p.Form); f != nil {
		r.Form = *f
	}
	return r
}

// ErrNobodyToAsk: the job runs on no job service (a runner called directly, in a test) and has nobody to ask.
var ErrNobodyToAsk = errors.New("the job has nobody to ask")

// AskWait is how long one wait for an answer lasts before the job asks again (the person may take hours: the job
// renews its lease meanwhile and learns of a cancel).
const AskWait = 25 * time.Second

// Ask asks the person the job runs for to fill in a form and waits for the values (checked against the form by the job
// service). It returns when the person answered, or with the error of ctx when the job is cancelled meanwhile.
func (j *Job) Ask(ctx context.Context, f form.Form) (map[string]any, error) {
	if j.queue == nil {
		return nil, ErrNobodyToAsk
	}
	if is := form.CheckForm(f); len(is) > 0 {
		return nil, fmt.Errorf("the form of the request: %w: %w", is, ErrInvalid)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	req := Request{ID: "req-" + hex.EncodeToString(b), Form: f, AskedAt: time.Now().UTC()}
	if err := j.queue.Ask(ctx, j.ID, j.worker, req); err != nil {
		return nil, err
	}
	what := f.Title
	if what == "" {
		what = "a question"
	}
	j.Progress("waiting for " + j.By + " to answer: " + what)
	for {
		values, ok, err := j.queue.Await(ctx, j.ID, j.worker, req.ID, AskWait)
		if err != nil {
			if ctx.Err() != nil {
				return nil, context.Cause(ctx)
			}
			return nil, err
		}
		if ok {
			return values, nil
		}
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
	}
}
