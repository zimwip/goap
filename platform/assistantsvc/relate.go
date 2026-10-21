package assistantsvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/platform/convsvc"
)

// Attaching and detaching the change of the frame (ADR 0106): on a change, the assistant may propose to attach it to a
// change integrating it (a release taking a candidate) or to detach it from the change integrating it (deferred,
// rejected). Like start_agent, the tool starts nothing: it records a proposal the person confirms (ConfirmAction),
// which then attaches or detaches the change as them.

const (
	ToolAttachChange = "attach_change"
	ToolDetachChange = "detach_change"
	// ActionAttachChange and ActionDetachChange are the proposals the tools record.
	ActionAttachChange = "attach_change"
	ActionDetachChange = "detach_change"
)

// isProposal reports whether an action type is a proposal the person decides.
func isProposal(t any) bool {
	switch t {
	case ActionStartAgent, ActionUITool, ActionAttachChange, ActionDetachChange:
		return true
	}
	return false
}

// relateChange records a proposal to attach the change of the frame to a parent, or to detach it.
func (t *turn) relateChange(ctx context.Context, attach bool, a args) (any, error) {
	for _, prev := range t.actions {
		if prev["type"] == ActionAttachChange || prev["type"] == ActionDetachChange {
			return nil, errors.New("one proposal per answer: the person decides the first before another is made")
		}
	}
	id := t.workChange(ctx)
	if id == "" {
		return nil, errors.New("no working change: the person (or open_change) must select the change to attach or detach")
	}
	ch, err := t.s.readChange(ctx, t.p, id)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"changeId": id, "project": ch.ProjectID.Key}
	var label string
	if attach {
		parentID := a.str("parent")
		if parentID == "" {
			return nil, errors.New(`"parent" is required: the id of the change integrating it (search_changes finds it)`)
		}
		if ch.ParentID != "" {
			return nil, fmt.Errorf("change %q is already a sub-change of %q: detach it first", id, ch.ParentID)
		}
		parent, err := t.s.readChange(ctx, t.p, parentID)
		if err != nil {
			return nil, err
		}
		if parent.ID == ch.ID {
			return nil, errors.New("a change is not attached to itself")
		}
		params["parentId"] = parentID
		label = fmt.Sprintf("Attach %q to %q", ch.Title, parent.Title)
	} else {
		if ch.ParentID == "" {
			return nil, fmt.Errorf("change %q is not attached to another change", id)
		}
		params["parentId"] = string(ch.ParentID)
		label = fmt.Sprintf("Detach %q from the change integrating it", ch.Title)
	}
	typ := ActionDetachChange
	if attach {
		typ = ActionAttachChange
	}
	act := convsvc.Action{"type": typ, "status": StatusProposed, "label": clip(label, 200), "args": params}
	if r := a.str("rationale"); r != "" {
		act["rationale"] = clip(r, 500)
	}
	t.actions = append(t.actions, act)
	return map[string]any{"status": StatusProposed, "note": "proposed, awaiting the person's confirmation in the interface: nothing changed yet. Say what you propose and why."}, nil
}

// confirmRelate carries out an accepted attach / detach proposal as the person: re-validated (stale when the change
// moved since), claimed, then done (with the change as it is now) or failed.
func (s *Service) confirmRelate(ctx context.Context, p authz.Principal, act convsvc.Action,
	decide func(from, to string, fn func(convsvc.Action)) (convsvc.Message, error)) (convsvc.Message, error) {
	ar := args(mapOf(act["args"]))
	ch, err := s.readChange(ctx, p, ar.str("changeId"))
	if err != nil {
		return convsvc.Message{}, fmt.Errorf("%w: %v", ErrStale, err)
	}
	attach := act["type"] == ActionAttachChange
	switch {
	case attach && ch.ParentID != "":
		return convsvc.Message{}, fmt.Errorf("%w: change %q is already a sub-change of %q", ErrStale, ch.ID, ch.ParentID)
	case !attach && string(ch.ParentID) != ar.str("parentId"):
		return convsvc.Message{}, fmt.Errorf("%w: change %q is no longer a sub-change of %q", ErrStale, ch.ID, ar.str("parentId"))
	}
	if _, err := decide(StatusProposed, StatusStarting, nil); err != nil {
		return convsvc.Message{}, err
	}
	var out domain.Change
	if attach {
		out, err = s.Graph.AttachChange(ctx, ch.ID, domain.ChangeID(ar.str("parentId")))
	} else {
		out, err = s.Graph.DetachChange(ctx, ch.ID)
	}
	if err != nil {
		return decide(StatusStarting, StatusFailed, func(a convsvc.Action) { a["error"] = clip(err.Error(), maxErrorText) })
	}
	return decide(StatusStarting, StatusDone, func(a convsvc.Action) {
		a["result"] = map[string]any{"changeId": string(out.ID), "parentId": string(out.ParentID)}
	})
}

// describeRelate is what the history tells the model of an attach / detach proposal.
func describeRelate(a convsvc.Action) string {
	ar := args(mapOf(a["args"]))
	what := fmt.Sprintf("attach change %s to %s", ar.str("changeId"), ar.str("parentId"))
	if a["type"] == ActionDetachChange {
		what = fmt.Sprintf("detach change %s from %s", ar.str("changeId"), ar.str("parentId"))
	}
	switch a["status"] {
	case StatusDone:
		return "the person accepted the proposal to " + what + ": done"
	case StatusRejected:
		return "the person rejected the proposal to " + what + ": nothing changed"
	case StatusFailed:
		return fmt.Sprintf("the person accepted the proposal to %s but it failed: %v", what, a["error"])
	}
	return "proposal to " + what + ": the person has not decided yet, nothing changed"
}
