package pbconv

import (
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/domain"
)

// RequestToPB converts a request with its links (ADR 0098); its status is the effective one.
func RequestToPB(r domain.Request) *graphv1.Request {
	out := &graphv1.Request{Id: string(r.ID), Title: r.Title, Text: r.Text, Requester: r.Requester, ProjectId: r.ProjectID, OriginKind: r.Origin.Kind,
		OriginRef: r.Origin.Ref, Status: string(r.Effective()), CreatedAt: Time(r.CreatedAt)}
	for _, l := range r.Links {
		out.Links = append(out.Links, &graphv1.RequestLink{ChangeId: string(l.Change), Role: string(l.Role), By: l.By, At: Time(l.At), ChangeStatus: string(l.ChangeStatus)})
	}
	return out
}

// RequestFromPB is the inverse of RequestToPB; a delivered status is stored triaged.
func RequestFromPB(r *graphv1.Request) domain.Request {
	if r == nil {
		return domain.Request{}
	}
	out := domain.Request{ID: domain.RequestID(r.Id), Title: r.Title, Text: r.Text, Requester: r.Requester, ProjectID: r.ProjectId,
		Origin: domain.RequestOrigin{Kind: r.OriginKind, Ref: r.OriginRef}, Status: domain.RequestStatus(r.Status), CreatedAt: FromTime(r.CreatedAt)}
	if out.Status == domain.RequestDelivered {
		out.Status = domain.RequestTriaged
	}
	for _, l := range r.Links {
		out.Links = append(out.Links, domain.RequestLink{Change: domain.ChangeID(l.ChangeId), Request: out.ID, Role: domain.LinkRole(l.Role), By: l.By, At: FromTime(l.At),
			ChangeStatus: domain.ChangeStatus(l.ChangeStatus)})
	}
	return out
}

// RequestEntriesToPB converts the log of a request.
func RequestEntriesToPB(es []domain.RequestEntry) []*graphv1.RequestEntry {
	out := make([]*graphv1.RequestEntry, len(es))
	for i, e := range es {
		out[i] = &graphv1.RequestEntry{Seq: e.Seq, Type: e.Type, By: e.By, At: Time(e.At), Payload: string(e.Payload)}
	}
	return out
}
