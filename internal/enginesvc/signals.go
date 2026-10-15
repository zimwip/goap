package enginesvc

import (
	"context"
	"encoding/json"

	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/engine"
)

// Subjects of the signals between engine replicas (ADR 0124): core NATS, at most once, outside the GOAP stream (a
// lost signal only delays the work: the shared locks stay the arbiter).
const (
	SubjectWake      = "engine.wake"
	SubjectInterrupt = "engine.interrupt"
)

// NATSSignals carries the signals between engine replicas over NATS (engine.Signals).
type NATSSignals struct{ Bus *platform.Events }

var _ engine.Signals = NATSSignals{}

type interruptSignal struct {
	ID string `json:"id"`
	By string `json:"by"`
}

// Wake implements engine.Signals.
func (s NATSSignals) Wake(context.Context) error { return s.Bus.PublishCore(SubjectWake, struct{}{}) }

// Interrupt implements engine.Signals.
func (s NATSSignals) Interrupt(_ context.Context, id, by string) error {
	return s.Bus.PublishCore(SubjectInterrupt, interruptSignal{ID: id, By: by})
}

// Listen hands the signals of the other replicas to e.
func (s NATSSignals) Listen(e *engine.Engine) error {
	if err := s.Bus.Subscribe(SubjectWake, func([]byte) { e.Woken() }); err != nil {
		return err
	}
	return s.Bus.Subscribe(SubjectInterrupt, func(data []byte) {
		var sig interruptSignal
		if json.Unmarshal(data, &sig) == nil && sig.ID != "" {
			e.Interrupted(sig.ID, sig.By)
		}
	})
}
