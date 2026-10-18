package convsvc

import (
	"context"
	"fmt"
	"time"

	"github.com/zimwip/goap/lib/outbox"
)

// OutboxOrigin names the conversations service in the ids of its events (ADR 0136).
const OutboxOrigin = "conversations"

// Events of a conversation, the last token of their subject goap.conversation.<id>.<event>.
const (
	EventCreated         = "created"
	EventRenamed         = "renamed"
	EventDeleted         = "deleted"
	EventMessageAppended = "message_appended"
	EventMessageUpdated  = "message_updated"
	EventActionUpdated   = "action_updated"
	EventSummarized      = "summarized"
)

// Subject is the subject of an event of a conversation.
func Subject(conversationID, event string) string {
	return "goap.conversation." + outbox.Token(conversationID) + "." + event
}

// Event is the payload of an event of a conversation (ADR 0136). It names what changed, never what it says: no text
// of a message, no title, no action's arguments. Subject is the owner of the conversation, to whom alone the event
// stream delivers it.
type Event struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversationId"`
	Subject        string `json:"subject"`
	// MessageID and Role name the message of a message or action event.
	MessageID string `json:"messageId,omitempty"`
	Role      string `json:"role,omitempty"`
	// ActionIndex is the index of the action an action_updated names (nil otherwise).
	ActionIndex *int `json:"actionIndex,omitempty"`
	// Status is the status of the message (message events) or of the action (action_updated) once written.
	Status string    `json:"status,omitempty"`
	At     time.Time `json:"at"`
}

// Emit makes the service write the events of the conversations in the outbox of its store, in the transaction of
// their fact (ADR 0136), and returns the relay delivering them to sink, in order, at least once: after each write, in
// the background, and again by Relay.Run, which the caller runs, for what a delivery missed. The store must be an
// outbox.Store (it panics otherwise). A nil sink returns nil and the service writes no event. Call it before serving.
func (s *Service) Emit(sink outbox.Sink) *outbox.Relay {
	if sink == nil {
		return nil
	}
	st, ok := s.Store.(outbox.Store)
	if !ok {
		panic(fmt.Sprintf("convsvc: the store %T keeps no outbox", s.Store))
	}
	s.relay = &outbox.Relay{Origin: OutboxOrigin, Store: st, Sink: sink}
	return s.relay
}

// Relay is the relay of the service's outbox (nil until Emit).
func (s *Service) Relay() *outbox.Relay { return s.relay }

// emitting reports whether the writes carry their events.
func (s *Service) emitting() bool { return s.relay != nil }

// event builds the message of an event (none when the service does not emit).
func (s *Service) event(ctx context.Context, ev Event) ([]outbox.Message, error) {
	if !s.emitting() {
		return nil, nil
	}
	m, err := outbox.New(ctx, Subject(ev.ConversationID, ev.Event), ev, ev.At)
	if err != nil {
		return nil, err
	}
	return []outbox.Message{m}, nil
}

// delivered delivers what a write put in the outbox, in the background: the caller does not wait for the consumers.
func (s *Service) delivered(ctx context.Context) {
	if relay := s.relay; relay != nil {
		go func() { _ = relay.Drain(context.WithoutCancel(ctx)) }()
	}
}

// messageEvents is the event of an update of a message: message_updated when anything of it changed, nothing else.
func (s *Service) messageEvents(ctx context.Context, at time.Time) MessageEvents {
	if !s.emitting() {
		return nil
	}
	return func(owner string, before, after Message) ([]outbox.Message, error) {
		if sameContent(before, after) {
			return nil, nil
		}
		return s.event(ctx, Event{Event: EventMessageUpdated, ConversationID: after.ConversationID, Subject: owner,
			MessageID: after.ID, Role: after.Role, Status: after.Status, At: at})
	}
}

// actionEvents is the event of an update of the action at index of a message: action_updated when it changed.
func (s *Service) actionEvents(ctx context.Context, index int, at time.Time) MessageEvents {
	if !s.emitting() {
		return nil
	}
	return func(owner string, before, after Message) ([]outbox.Message, error) {
		if index < len(before.Actions) && index < len(after.Actions) && actionsEqual(before.Actions[index:index+1], after.Actions[index:index+1]) {
			return nil, nil
		}
		var status string
		if index < len(after.Actions) {
			status, _ = after.Actions[index]["status"].(string)
		}
		return s.event(ctx, Event{Event: EventActionUpdated, ConversationID: after.ConversationID, Subject: owner,
			MessageID: after.ID, Role: after.Role, ActionIndex: &index, Status: status, At: at})
	}
}

func sameContent(a, b Message) bool {
	return a.Text == b.Text && a.Status == b.Status && a.Error == b.Error && a.ProcessID == b.ProcessID &&
		a.Context == b.Context && actionsEqual(a.Actions, b.Actions)
}

// actionsEqual compares two lists of actions as stored (JSON), so a nil and an empty list are the same.
func actionsEqual(a, b []Action) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	x, err1 := encodeActions(a)
	y, err2 := encodeActions(b)
	return err1 == nil && err2 == nil && x == y
}
