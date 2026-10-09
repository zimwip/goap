package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/changeapi"
	"github.com/zimwip/goap/pkg/domain"
)

// Requests (ADR 0098): the origin of a piece of work, an object of the change component that may exist before any
// change and is linked to the changes that answer it (many to many). The graph keeps the mechanism: the request, its
// links, its project (set when it is first linked, followed by the moves of its changes) and its log; who may see or act
// on one is the service's.

// NewRequest is changeapi.NewRequest (ADR 0098: the contract of the change, shared with the engine).
type NewRequest = changeapi.NewRequest

// Kinds of the change log entries of the links of a change to its requests (stream change).
const (
	LogRequestLinked   = domain.LogChange + ".request_linked"
	LogRequestUnlinked = domain.LogChange + ".request_unlinked"
)

// CreateRequest records a request, open.
func (g *Graph) CreateRequest(ctx context.Context, in NewRequest) (r domain.Request, err error) {
	if strings.TrimSpace(in.Title) == "" {
		return r, invalidf("a request needs a title")
	}
	if in.Origin.Kind == "" {
		in.Origin.Kind = domain.OriginManual
	}
	if !slices.Contains(domain.RequestOrigins, in.Origin.Kind) {
		return r, invalidf("unknown origin %q (%s)", in.Origin.Kind, strings.Join(domain.RequestOrigins, ", "))
	}
	if in.Requester == "" {
		in.Requester = g.caller(ctx)
	}
	if in.Requester == "" {
		return r, invalidf("a request needs a requester")
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if in.ProjectID != "" {
			if _, err := g.structureNode(ctx, tx, domain.StructureProject, in.ProjectID); err != nil {
				return fmt.Errorf("project of the request: %w", err)
			}
		}
		r = domain.Request{ID: domain.RequestID(g.newID()), Title: strings.TrimSpace(in.Title), Text: in.Text, Requester: in.Requester, ProjectID: in.ProjectID,
			Origin: in.Origin, Status: domain.RequestOpen, CreatedAt: g.now()}
		if err := tx.PutRequest(ctx, r); err != nil {
			return err
		}
		return g.requestLog(ctx, tx, r.ID, domain.RequestCreated, map[string]any{"title": r.Title, "text": r.Text, "origin": r.Origin, "projectId": r.ProjectID})
	})
	return
}

// Request returns a request with its links.
func (g *Graph) Request(ctx context.Context, id domain.RequestID) (r domain.Request, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		r, err = requestTx(ctx, tx, id)
		return err
	})
	return
}

func requestTx(ctx context.Context, tx Tx, id domain.RequestID) (domain.Request, error) {
	r, err := tx.Request(ctx, id)
	if err != nil {
		return r, err
	}
	r.Links, err = tx.RequestLinks(ctx, true, string(id))
	return r, err
}

// Requests lists the requests matching f with their links, oldest first; the statuses are the effective ones (a
// request whose linked changes are all applied is delivered).
func (g *Graph) Requests(ctx context.Context, f domain.RequestFilter) (out []domain.Request, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		rs, err := tx.Requests(ctx, f)
		if err != nil {
			return err
		}
		for _, r := range rs {
			if r.Links, err = tx.RequestLinks(ctx, true, string(r.ID)); err != nil {
				return err
			}
			if !f.Match(r) {
				continue
			}
			out = append(out, r)
			if f.Limit > 0 && len(out) == f.Limit {
				break
			}
		}
		return nil
	})
	return
}

// UpdateRequest renames a request (its text is never rewritten); a request in a final status is not edited.
func (g *Graph) UpdateRequest(ctx context.Context, id domain.RequestID, title string) (r domain.Request, err error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return r, invalidf("a request needs a title")
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if r, err = requestTx(ctx, tx, id); err != nil {
			return err
		}
		if r.Status.Final() {
			return fmt.Errorf("request %s is %s: %w", id, r.Status, ErrConflict)
		}
		if r.Title == title {
			return nil
		}
		from := r.Title
		r.Title = title
		if err := tx.PutRequest(ctx, r); err != nil {
			return err
		}
		return g.requestLog(ctx, tx, id, domain.RequestUpdated, map[string]any{"title": map[string]any{"from": from, "to": title}})
	})
	return
}

// SetRequestStatus closes, rejects or withdraws a request, with a comment: a final status a request does not leave.
// Who may set which is the service's (the requester or a triager closes, the requester withdraws).
func (g *Graph) SetRequestStatus(ctx context.Context, id domain.RequestID, status domain.RequestStatus, comment string) (r domain.Request, err error) {
	if !status.Final() {
		return r, invalidf("a request is closed, rejected or withdrawn, not set %s", status)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if r, err = requestTx(ctx, tx, id); err != nil {
			return err
		}
		if r.Status.Final() {
			return fmt.Errorf("request %s is %s: %w", id, r.Status, ErrConflict)
		}
		from := r.Effective()
		r.Status = status
		if err := tx.PutRequest(ctx, r); err != nil {
			return err
		}
		return g.requestLog(ctx, tx, id, domain.RequestStatusSet, map[string]any{"from": from, "to": status, "comment": comment})
	})
	return
}

// LinkRequest links a request to a change that answers it (origin, amends or covers). The change is a root change
// (the links stay on the parent of a family) and acts in the project of the request: an untriaged request takes the
// project of the change, and is triaged from its first link. A request in a final status is not linked.
func (g *Graph) LinkRequest(ctx context.Context, id domain.RequestID, change domain.ChangeID, role domain.LinkRole) (r domain.Request, err error) {
	if !domain.ValidLinkRole(role) {
		return r, invalidf("unknown link role %q (origin, amends, covers)", role)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if r, err = requestTx(ctx, tx, id); err != nil {
			return err
		}
		if r.Status.Final() {
			return fmt.Errorf("request %s is %s: %w", id, r.Status, ErrConflict)
		}
		c, err := tx.Change(ctx, change)
		if err != nil {
			return err
		}
		if c.ParentID != "" {
			return invalidf("change %s is a sub-change: a request is linked to the root of its family (%s)", change, c.ParentID)
		}
		if r.ProjectID != "" && r.ProjectID != c.ProjectID {
			return invalidf("change %s acts in project %s, not in %s, the project of request %s", change, c.ProjectID, r.ProjectID, id)
		}
		by, at := g.caller(ctx), g.now()
		if err := tx.PutRequestLink(ctx, domain.RequestLink{Change: change, Request: id, Role: role, By: by, At: at}); err != nil {
			return err
		}
		triage := map[string]any{}
		if r.ProjectID == "" {
			r.ProjectID, triage["projectId"] = c.ProjectID, c.ProjectID
		}
		if r.Status == domain.RequestOpen {
			r.Status, triage["status"] = domain.RequestTriaged, domain.RequestTriaged
		}
		if len(triage) > 0 {
			if err := tx.PutRequest(ctx, r); err != nil {
				return err
			}
		}
		if err := g.requestLog(ctx, tx, id, domain.RequestLinked, map[string]any{"changeId": change, "role": role, "triage": triage}); err != nil {
			return err
		}
		if err := g.changeRequestLog(ctx, tx, change, LogRequestLinked, id, map[string]any{"requestId": id, "role": role}); err != nil {
			return err
		}
		r.Links, err = tx.RequestLinks(ctx, true, string(id))
		return err
	})
	return
}

// UnlinkRequest removes the link of a request to a change; a triaged request left with no link is open again (it keeps
// its project).
func (g *Graph) UnlinkRequest(ctx context.Context, id domain.RequestID, change domain.ChangeID) (r domain.Request, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if r, err = requestTx(ctx, tx, id); err != nil {
			return err
		}
		if err := g.unlinkTx(ctx, tx, r, change, ""); err != nil {
			return err
		}
		r, err = requestTx(ctx, tx, id)
		return err
	})
	return
}

// unlinkTx removes the link of r to change, logs it on both sides (reason: why, when the graph unlinks by itself) and
// reopens a triaged request left with no link.
func (g *Graph) unlinkTx(ctx context.Context, tx Tx, r domain.Request, change domain.ChangeID, reason string) error {
	if err := tx.DeleteRequestLink(ctx, change, r.ID); err != nil {
		return err
	}
	payload := map[string]any{"changeId": change}
	if reason != "" {
		payload["reason"] = reason
	}
	left, err := tx.RequestLinks(ctx, true, string(r.ID))
	if err != nil {
		return err
	}
	if len(left) == 0 && r.Status == domain.RequestTriaged {
		r.Status, payload["status"] = domain.RequestOpen, domain.RequestOpen
		if err := tx.PutRequest(ctx, r); err != nil {
			return err
		}
	}
	if err := g.requestLog(ctx, tx, r.ID, domain.RequestUnlinked, payload); err != nil {
		return err
	}
	if reason != "" { // the change is going: its log goes with it
		return nil
	}
	return g.changeRequestLog(ctx, tx, change, LogRequestUnlinked, r.ID, map[string]any{"requestId": r.ID})
}

// RequestLog returns the log of a request.
func (g *Graph) RequestLog(ctx context.Context, id domain.RequestID) (out []domain.RequestEntry, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if _, err := tx.Request(ctx, id); err != nil {
			return err
		}
		out, err = tx.RequestLog(ctx, id)
		return err
	})
	return
}

func (g *Graph) requestLog(ctx context.Context, tx Tx, id domain.RequestID, typ string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.AppendRequestLog(ctx, domain.RequestEntry{Request: id, Type: typ, By: g.caller(ctx), At: g.now(), Payload: raw})
	return err
}

func (g *Graph) changeRequestLog(ctx context.Context, tx Tx, change domain.ChangeID, typ string, request domain.RequestID, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.AppendLog(ctx, domain.LogEntry{ID: g.newID(), Change: change, Type: typ, Subject: string(request), By: g.caller(ctx), At: g.now(), Payload: raw})
	return err
}

// moveRequestsTx moves the requests of a family of changes moving to project (ADR 0091, 0098): a request linked only
// to changes of the family takes the project; one also linked to a change staying behind refuses the move.
func (g *Graph) moveRequestsTx(ctx context.Context, tx Tx, family []domain.Change, project string) error {
	in := map[domain.ChangeID]bool{}
	for _, c := range family {
		in[c.ID] = true
	}
	done := map[domain.RequestID]bool{}
	for _, c := range family {
		links, err := tx.RequestLinks(ctx, false, string(c.ID))
		if err != nil {
			return err
		}
		for _, l := range links {
			if done[l.Request] {
				continue
			}
			done[l.Request] = true
			all, err := tx.RequestLinks(ctx, true, string(l.Request))
			if err != nil {
				return err
			}
			for _, o := range all {
				if !in[o.Change] {
					return fmt.Errorf("request %s is also linked to change %s, which stays in its project: %w", l.Request, o.Change, ErrConflict)
				}
			}
			r, err := tx.Request(ctx, l.Request)
			if err != nil {
				return err
			}
			if r.ProjectID == project {
				continue
			}
			from := r.ProjectID
			r.ProjectID = project
			if err := tx.PutRequest(ctx, r); err != nil {
				return err
			}
			if err := g.requestLog(ctx, tx, r.ID, domain.RequestMoved, map[string]any{"from": from, "to": project, "changeId": c.ID}); err != nil {
				return err
			}
		}
	}
	return nil
}

// unlinkPurgedTx removes the links of a change about to be purged, logged on its requests.
func (g *Graph) unlinkPurgedTx(ctx context.Context, tx Tx, change domain.ChangeID) error {
	links, err := tx.RequestLinks(ctx, false, string(change))
	if err != nil {
		return err
	}
	for _, l := range links {
		r, err := tx.Request(ctx, l.Request)
		if err != nil {
			return err
		}
		if err := g.unlinkTx(ctx, tx, r, change, "purged"); err != nil {
			return err
		}
	}
	return nil
}
