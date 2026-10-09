package builtin

import (
	"context"
	"strings"

	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
)

// requestText is the length of the text of a request a listing keeps.
const requestText = 400

// mayRequest checks an action on a request (ADR 0098, access.ResourceRequest): in process the connector reads the graph
// directly, so the handler's check does not stand in front of it. Without an authorizer only the platform APIs
// authorize.
func (c Change) mayRequest(ctx context.Context, who authz.Principal, r domain.Request, action string) error {
	if c.p.Authz == nil {
		return nil
	}
	return authz.Check(ctx, c.p.Authz, authz.Request{Subject: who, Action: action,
		Resource: authz.Resource{Type: access.ResourceRequest, ID: string(r.ID), Name: r.Title, Owner: r.Requester, ProjectID: r.ProjectID}})
}

// requestSummary is a request as the tools return it, its text cut.
func requestSummary(r domain.Request) map[string]any {
	text := r.Text
	if len([]rune(text)) > requestText {
		text = string([]rune(text)[:requestText]) + "…"
	}
	links := make([]map[string]any, 0, len(r.Links))
	for _, l := range r.Links {
		links = append(links, map[string]any{"change": l.Change, "role": l.Role, "status": l.ChangeStatus})
	}
	return map[string]any{"id": r.ID, "title": r.Title, "text": text, "status": r.Effective(), "requester": r.Requester,
		"project": r.ProjectID, "origin": r.Origin, "links": links}
}

// requests serves the request tools of goap-change: the origin of the work a change answers (ADR 0098). An intake
// looks for the open requests a new one repeats, links the change it opens to the request it answers, or records
// one a change raises.
func (c Change) requests(ctx context.Context, who authz.Principal, op string, a args) (map[string]any, error) {
	switch op {
	case "requests":
		f := domain.RequestFilter{}
		if a.boolean("linked") {
			id, err := changeID(ctx, a)
			if err != nil {
				return nil, err
			}
			f.Change = id
		} else {
			f.Statuses = []domain.RequestStatus{domain.RequestOpen, domain.RequestTriaged}
		}
		if st := a.str("status"); st != "" {
			f.Statuses = []domain.RequestStatus{domain.RequestStatus(st)}
		}
		if p := a.str("project"); p != "" {
			f.Projects = []string{p}
		}
		if a.boolean("mine") {
			f.Requester = who.Subject
		}
		rs, err := c.p.Graph.Requests(ctx, f)
		if err != nil {
			return nil, err
		}
		q := strings.ToLower(strings.TrimSpace(a.str("q")))
		list := []map[string]any{}
		truncated := false
		for _, r := range rs {
			if q != "" && !strings.Contains(strings.ToLower(r.Title+"\n"+r.Text), q) {
				continue
			}
			if c.mayRequest(ctx, who, r, "view") != nil {
				continue
			}
			if len(list) == a.limit() {
				truncated = true
				break
			}
			list = append(list, requestSummary(r))
		}
		return result(map[string]any{"requests": list, "truncated": truncated})
	case "request":
		title, err := a.required("title")
		if err != nil {
			return nil, err
		}
		in := graph.NewRequest{Title: title, Text: a.str("text"), Requester: who.Subject, ProjectID: a.str("project")}
		if ch := mcp.CallFrom(ctx).Change; ch != "" {
			// a request a run raises comes from the change it works on
			in.Origin = domain.RequestOrigin{Kind: domain.OriginChange, Ref: ch}
		}
		if err := c.mayRequest(ctx, who, domain.Request{Requester: who.Subject, ProjectID: in.ProjectID}, "create"); err != nil {
			return nil, err
		}
		r, err := c.p.Graph.CreateRequest(ctx, in)
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"request": requestSummary(r)})
	default: // link_request
		rid, err := a.required("request")
		if err != nil {
			return nil, err
		}
		id, err := changeID(ctx, a)
		if err != nil {
			return nil, err
		}
		r, err := c.p.Graph.Request(ctx, domain.RequestID(rid))
		if err != nil {
			return nil, err
		}
		if err := c.mayRequest(ctx, who, r, "link"); err != nil {
			return nil, err
		}
		role := domain.LinkRole(a.str("role"))
		if role == "" {
			role = domain.LinkCovers
		}
		r, err = c.p.Graph.LinkRequest(ctx, r.ID, id, role)
		if err != nil {
			return nil, err
		}
		return result(map[string]any{"request": requestSummary(r)})
	}
}
