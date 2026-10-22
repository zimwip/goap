// Package events names the events a trigger or a transverse methodology can react to (ADR 0036 §3), the one list the
// engine, the graph service and the methodology compiler share. It imports nothing of the project.
package events

import "strings"

// The events of changes, processes and methodologies, as triggers (`event:`) and subscriptions (`on:`) name them.
const (
	ChangeCreated   = "change.created"
	ChangeApplied   = "change.applied"
	ChangeItemAdded = "change.item_added"
	ChangeSignal    = "change.signal"
	// ChangeTransitioned, ChangeCommitted, ChangeAttached and ChangeDetached couple the changes of methodologies
	// integrating one another (ADR 0106): a lifecycle move, a commit on its branch, an attachment to a change
	// integrating it, a detachment from it.
	ChangeTransitioned = "change.transitioned"
	ChangeCommitted    = "change.committed"
	// ChangeMerged: a change merged into the branch it was forked from (a sub-change into its parent's, a root change
	// into a lasting branch); like change.committed it names the branch and the node versions it left there (ADR 0159).
	ChangeMerged = "change.merged"
	// ChangeRebased: the branch of a change was moved onto the head of what it is forked from, with the node versions
	// the commit of its merges wrote (ADR 0159).
	ChangeRebased  = "change.rebased"
	ChangeAttached = "change.attached"
	ChangeDetached = "change.detached"
	// ChangeEdited: a field of the header was edited (title, intent, goal, data), the fields in event.fields
	// (ADR 0110).
	ChangeEdited         = "change.edited"
	ProcessCompleted     = "process.completed"
	ProcessFailed        = "process.failed"
	ProcessStuck         = "process.stuck"
	ProcessAttached      = "process.attached"
	StepCompleted        = "step.completed"
	MethodologyPublished = "methodology.published"
)

// ChangePurged: a change that committed nothing was removed with its log (ADR 0037). The web forgets it; it is not an
// event a trigger reacts to (not in All).
const ChangePurged = "change.purged"

// All lists the events a trigger can react to (methodology.TriggerEvents); the web keeps one copy of it
// (TRIGGER_EVENTS in web/src/lib/api/types/registry.ts), checked against this list by a test.
var All = []string{
	ChangeCreated, ChangeApplied, ChangeItemAdded, ChangeSignal,
	ChangeTransitioned, ChangeCommitted, ChangeMerged, ChangeRebased, ChangeAttached, ChangeDetached, ChangeEdited,
	ProcessCompleted, ProcessFailed, ProcessStuck, ProcessAttached,
	StepCompleted, MethodologyPublished,
}

// The names the process broker gives some of its events (engine.ProcessEvent.Event); the others are the process
// statuses ("completed", "failed", "stuck").
const (
	BrokerAttached      = "attached"
	BrokerStepCompleted = "step_completed"
)

// FromBroker maps the name of a process broker event to the event triggers see, for the two that are not a status
// (the one place the two vocabularies meet).
func FromBroker(name string) (string, bool) {
	switch name {
	case BrokerAttached:
		return ProcessAttached, true
	case BrokerStepCompleted:
		return StepCompleted, true
	}
	return "", false
}

// ChangeSubject is the broker subject a change event is published on: goap.change.<change>.<event without its
// "change." prefix>.
func ChangeSubject(change, event string) string {
	return "goap.change." + change + "." + strings.TrimPrefix(event, "change.")
}
