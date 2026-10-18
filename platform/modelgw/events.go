package modelgw

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/zimwip/goap/lib/outbox"
)

// The events of the gateway (ADR 0136): the gateway owns the ledger of calls, so it emits their events, written in its
// outbox (table model_outbox) in the transaction that appends the row, and delivered by its relay. An event names the
// call, who made it, what it cost and how it ended; it never carries the exchange (prompt, answer) nor the text of a
// behaviour.

// OutboxOrigin names the gateway in the ids of its events.
const OutboxOrigin = "modelgw"

// Outcomes of a call.
const (
	OutcomeServed  = "served"
	OutcomeRefused = "refused" // by the admission: unknown model, disabled, roles, quota
	OutcomeFailed  = "failed"  // admitted, then failed (provider, network)
)

// CallSubject is the subject of the event of a ledger row: goap.model.call.<seq>.recorded.
func CallSubject(seq int64) string {
	return "goap.model.call." + strconv.FormatInt(seq, 10) + ".recorded"
}

// QuotaSubject is the subject of the event of a call refused for its quota: goap.model.quota.<subject>.exceeded (the
// subject as one token, outbox.Token; "_" for a caller without identity).
func QuotaSubject(subject string) string {
	return "goap.model.quota." + cmp.Or(outbox.Token(subject), "_") + ".exceeded"
}

// CallRecorded is the payload of goap.model.call.<seq>.recorded: a row of the ledger, its exchange and its behaviours
// aside. Calibration calls are rows too (Source llm.SourceCalibration).
type CallRecorded struct {
	Seq        int64     `json:"seq"`
	At         time.Time `json:"at"`
	DurationMs int64     `json:"durationMs"`
	Subject    string    `json:"subject"`
	EndUser    string    `json:"endUser,omitempty"`
	Project    string    `json:"project,omitempty"`
	Org        string    `json:"org,omitempty"`
	Source     string    `json:"source,omitempty"`
	Kind       string    `json:"kind"`
	// Alias is the name requested, "" for a literal provider/model.
	Alias          string `json:"alias,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	InputTokens    int64  `json:"inputTokens"`
	OutputTokens   int64  `json:"outputTokens"`
	BehaviorTokens int64  `json:"behaviorTokens,omitempty"`
	// Outcome is OutcomeServed, OutcomeRefused or OutcomeFailed.
	Outcome        string `json:"outcome"`
	ProcessID      string `json:"processId,omitempty"`
	ChangeID       string `json:"changeId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
}

// QuotaExceeded is the payload of goap.model.quota.<subject>.exceeded: a call refused because the quota of its model
// is spent. Seq is the ledger row of the refused call.
type QuotaExceeded struct {
	Seq      int64     `json:"seq"`
	At       time.Time `json:"at"`
	Subject  string    `json:"subject"`
	EndUser  string    `json:"endUser,omitempty"`
	Source   string    `json:"source,omitempty"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	// Limit is the quota of the model (tokens), Used what was counted, Period its period (day, month, total).
	Limit  int64  `json:"limit"`
	Used   int64  `json:"used"`
	Period string `json:"period"`
}

// quotaError is the refusal of a call whose model spent its quota (errors.Is ErrQuotaExceeded).
type quotaError struct {
	provider, model, period string
	used, limit             int64
}

func (e *quotaError) Error() string {
	return fmt.Sprintf("%s: %s/%s used %d of %d tokens (%s)", ErrQuotaExceeded, e.provider, e.model, e.used, e.limit, e.period)
}

func (e *quotaError) Unwrap() error { return ErrQuotaExceeded }

// CallEvents builds the events of a ledger row once the store knows its seq (c.Seq), in the transaction appending it.
type CallEvents func(c Call) ([]outbox.Message, error)

// EmittingStore is a Store keeping the outbox of the gateway: AppendCallEmitting is AppendCall writing the events of
// the row (events, when not nil) in the same transaction; a row not written writes no event.
type EmittingStore interface {
	Store
	outbox.Store
	AppendCallEmitting(ctx context.Context, c Call, x *Exchange, events CallEvents) (int64, error)
}

// Emit makes the gateway write the events of its ledger in the outbox of its store, with each row (ADR 0136), and
// returns the relay delivering them to sink, in order, at least once: after each row, in the background, and again by
// Relay.Run, which the caller runs, for what a delivery missed. The store must be an EmittingStore (it panics
// otherwise); a nil sink: no outbox, no event (nil relay). Call it before serving.
func (s *Service) Emit(sink outbox.Sink) *outbox.Relay {
	if sink == nil {
		return nil
	}
	st, ok := s.Store.(EmittingStore)
	if !ok {
		panic(fmt.Sprintf("modelgw: the store %T keeps no outbox", s.Store))
	}
	s.relay = &outbox.Relay{Origin: OutboxOrigin, Store: st, Sink: sink, Log: s.Log}
	return s.relay
}

// Relay is the relay of the gateway's outbox (nil until Emit).
func (s *Service) Relay() *outbox.Relay { return s.relay }

// callEvents returns the events of a row: its record, and the quota refusal when it is one.
func callEvents(ctx context.Context, outcome string, quota *quotaError) CallEvents {
	return func(c Call) ([]outbox.Message, error) {
		m, err := outbox.New(ctx, CallSubject(c.Seq), CallRecorded{Seq: c.Seq, At: c.At, DurationMs: c.DurationMs, Subject: c.Subject, EndUser: c.EndUser,
			Project: c.Project, Org: c.Org, Source: c.Source, Kind: c.Kind, Alias: c.Alias, Provider: c.Provider, Model: c.Model,
			InputTokens: c.InputTokens, OutputTokens: c.OutputTokens, BehaviorTokens: c.BehaviorTokens, Outcome: outcome,
			ProcessID: c.ProcessID, ChangeID: c.ChangeID, ConversationID: c.ConversationID}, c.At)
		if err != nil {
			return nil, err
		}
		msgs := []outbox.Message{m}
		if quota != nil {
			q, err := outbox.New(ctx, QuotaSubject(c.Subject), QuotaExceeded{Seq: c.Seq, At: c.At, Subject: c.Subject, EndUser: c.EndUser, Source: c.Source,
				Provider: quota.provider, Model: quota.model, Limit: quota.limit, Used: quota.used, Period: quota.period}, c.At)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, q)
		}
		return msgs, nil
	}
}

// appendCall writes a row of the ledger: with its events and their delivery once Emit was called, else as before.
func (s *Service) appendCall(ctx context.Context, c Call, x *Exchange, outcome string, cause error) (int64, error) {
	relay := s.relay
	if relay == nil {
		return s.Store.AppendCall(ctx, c, x)
	}
	var quota *quotaError
	if outcome == OutcomeRefused {
		errors.As(cause, &quota)
	}
	seq, err := s.Store.(EmittingStore).AppendCallEmitting(ctx, c, x, callEvents(ctx, outcome, quota))
	if err != nil {
		return seq, err
	}
	// the delivery never holds the call: it runs after the commit, in the background
	s.drains.Add(1)
	go func() {
		defer s.drains.Done()
		_ = relay.Drain(context.WithoutCancel(ctx))
	}()
	return seq, nil
}
