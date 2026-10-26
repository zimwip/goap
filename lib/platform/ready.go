package platform

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"
)

// The announcements of the services others wait for at their start (ADR 0169): published once a service listens,
// on core NATS outside the GOAP stream (at most once, nothing kept).
const (
	// ReadyDomains: the domain registry serves, its built-in and shipped domains published.
	ReadyDomains = "service.ready.domains"
	// ReadyGraph: the platform graph serves (a content graph announces ReadyGraphContent).
	ReadyGraph        = "service.ready.graph.platform"
	ReadyGraphContent = "service.ready.graph.content"
)

// AwaitMaxDelay bounds the delay between two tries of Await: the fallback of an announcement lost or never made (no
// bus, a service of another version).
const AwaitMaxDelay = 30 * time.Second

// Announce publishes subject once the server listens (ADR 0169), so a service waiting for this one tries again at
// once rather than at its next delay. No bus: nothing.
func (s *Server) Announce(e *Events, subject string) {
	if e == nil {
		return
	}
	s.listening = append(s.listening, func() {
		if err := e.PublishCore(subject, map[string]string{"service": ServiceName()}); err != nil {
			s.log.Warn("announce", "subject", subject, "err", err)
		}
	})
}

// Await calls try until it succeeds (ADR 0169): at once, then again as soon as a message reaches one of the subjects
// (the service it waits for announcing it serves, or an event saying what it waits for is there), else after a delay
// doubling from a second up to AwaitMaxDelay. The subjects are subscribed before the first try, so an announcement
// made while it tries is not missed. retry, when set, is told each failure and the delay before the next try. It
// returns the error of ctx when ctx ends first.
func (e *Events) Await(ctx context.Context, subjects []string, try func(context.Context) error, retry func(err error, delay time.Duration)) error {
	wake := make(chan struct{}, 1)
	if e != nil {
		for _, subject := range subjects {
			sub, err := e.nc.Subscribe(subject, func(*nats.Msg) {
				select {
				case wake <- struct{}{}:
				default:
				}
			})
			if err == nil {
				defer func() { _ = sub.Unsubscribe() }()
			}
		}
	}
	for delay := time.Second; ; delay = min(2*delay, AwaitMaxDelay) {
		err := try(ctx)
		if err == nil {
			return nil
		}
		if retry != nil {
			retry(err, delay)
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-wake:
			delay = time.Second / 2 // the service is there now: a failure again is retried soon
		case <-t.C:
		}
		t.Stop()
	}
}
