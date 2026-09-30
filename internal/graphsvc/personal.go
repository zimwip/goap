package graphsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
)

// Personal changes (ADR 0037): a change held by the personal unit of a person (USR:<subject>) belongs to that
// person only: nobody else reads it, writes to it, applies or removes it, and it is never split into sub-changes.

// OwnerMe, as the owner_org of a new change, designates the personal unit of the caller.
const OwnerMe = "@me"

func subjectOf(ctx context.Context) string { return authz.From(ctx).Subject }

func denyPersonal(id domain.ChangeID) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("change %s: not found", id))
}

// resolveOwner turns "@me" into the personal unit of the caller, creating their User node on first use, and
// refuses the personal unit of another person.
func (h *Handler) resolveOwner(ctx context.Context, owner string) (string, error) {
	switch {
	case owner == OwnerMe:
		me := subjectOf(ctx)
		if me == "" {
			return "", connect.NewError(connect.CodeUnauthenticated, errors.New("personal changes need an identified caller"))
		}
		return domain.PersonalUnit(me), rpcerr.ToConnect(EnsureUser(ctx, h.Graph, me))
	case domain.IsPersonalUnit(owner) && domain.PersonalSubject(owner) != subjectOf(ctx):
		return "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("unit %s is personal to someone else", owner))
	}
	return owner, nil
}

// EnsureUser makes sure the organisation@User node of a subject exists, creating a minimal one (subject
// only) when it does not (ADR 0039: a user is created automatically, not by an administrator by hand, so
// they can be assigned roles and appear in the organisation navigation as soon as they are seen). Its key
// (access.UserKey, "USR:<subject>") is also domain.PersonalUnit's: since organisation@User extends
// organisation@OrgUnit (ADR 0039), the same node doubles as the personal unit that holds a subject's
// personal changes (ADR 0037) — one node, not two competing for the same key.
func EnsureUser(ctx context.Context, g *graph.Graph, subject string) error {
	if subject == "" {
		return nil
	}
	key := access.UserKey(subject)
	if _, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, key); err == nil {
		return nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return err
	}
	_, err := g.CreateNode(ctx, graph.NewNode{Namespace: mcp.NamespaceOrganisation, Key: key, Type: access.NodeTypeUser,
		Properties: access.User{Subject: subject}.Props()})
	if errors.Is(err, graph.ErrConflict) { // created meanwhile by a concurrent request
		return nil
	}
	return err
}

// EnsureCaller is the interceptor that calls EnsureUser for every authenticated caller (ADR 0039),
// deduplicated per process: a subject already seen is not checked again, so this costs one NodeByKey (and,
// the first time only, one write) per subject per process lifetime, not per call.
func (h *Handler) EnsureCaller() connect.Interceptor {
	var seen sync.Map // subject -> struct{}
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			who := h.Identity.Context(ctx, req.Header())
			if subject := subjectOf(who); subject != "" {
				if _, ok := seen.Load(subject); !ok {
					if err := EnsureUser(who, h.Graph, subject); err == nil {
						seen.Store(subject, struct{}{})
					}
				}
			}
			return next(ctx, req)
		}
	})
}

// PersonalScope is the interceptor that keeps personal changes to their owner: a request naming a change
// (change_id, or id for GetChange and UpdateChange) that is personal to someone else is answered as not found.
func (h *Handler) PersonalScope() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if msg, ok := req.Any().(proto.Message); ok {
				if id := changeIDIn(req.Spec().Procedure, msg); id != "" {
					who := h.Identity.Context(ctx, req.Header())
					if c, err := h.Graph.Change(who, id); err == nil && c.Personal() && !c.PersonalTo(subjectOf(who)) {
						return nil, denyPersonal(id)
					}
				}
			}
			return next(ctx, req)
		}
	})
}

// changeIDIn returns the change a request names.
func changeIDIn(procedure string, msg proto.Message) domain.ChangeID {
	fields := msg.ProtoReflect().Descriptor().Fields()
	name := protoreflect.Name("change_id")
	if strings.HasSuffix(procedure, "/GetChange") || strings.HasSuffix(procedure, "/UpdateChange") {
		name = "id"
	}
	fd := fields.ByName(name)
	if fd == nil || fd.Kind() != protoreflect.StringKind || fd.IsList() {
		return ""
	}
	return domain.ChangeID(msg.ProtoReflect().Get(fd).String())
}

// visibleTo reports whether the caller may see a change: every change but the personal ones of others.
func visibleTo(ctx context.Context, c domain.Change) bool {
	return !c.Personal() || c.PersonalTo(subjectOf(ctx))
}

func (h *Handler) DeleteChange(ctx context.Context, r *connect.Request[graphv1.DeleteChangeRequest]) (*connect.Response[graphv1.DeleteChangeResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, err := h.Graph.PurgeChange(ctx, domain.ChangeID(r.Msg.ChangeId))
	if err == nil {
		c.Items, c.Nodes = nil, nil
		h.publish(ctx, "goap.change."+string(c.ID)+".purged", domain.ChangeEvent{Type: "change.purged", Change: c})
	}
	return res(&graphv1.DeleteChangeResponse{Change: pbconv.ChangeToPB(c)}, err)
}
