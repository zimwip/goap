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

// EnsureUser makes sure the organisation@User node of a subject exists, creating one when it does not (ADR
// 0039: a user is created automatically, not by an administrator by hand, so they can be assigned roles and
// appear in the organisation navigation as soon as they are seen). Its key (access.UserKey,
// "USR:<subject>") is also domain.PersonalUnit's: since organisation@User extends organisation@OrgUnit (ADR
// 0039), the same node doubles as the personal unit that holds a subject's personal changes (ADR 0037) —
// one node, not two competing for the same key.
//
// A new user is linked member_of the unit new users join (NewUserUnit: the waiting unit an administrator
// flagged, else the default organisation, ADR 0042), and granted the admin role, through a platform
// Assignment (ADR 0046, 0047), when it is the very first User node the namespace has ever held (ADR 0040): a
// fresh deployment otherwise has no path to a first administrator at all. Creations are serialized within a
// process; a benign race between two processes' simultaneous first connections can grant admin to more than
// one subject; it never grants it to none, which is what the floor policy (ADR 0020) relies on. The User
// node, its member_of, and the first user's admin Assignment are committed together, as one change on main
// (createUser, ADR 0042).
//
// The default org is resolved *before* the node is created: SeedDefaults can still be seeding at startup (it
// waits on the registry to publish the type catalogue), and a caller seen in that window must not leave a
// permanently broken User behind — one with no member_of and no admin, since a node once created would make
// every later subject see the namespace as "already has a User" and never get the first-admin bootstrap
// either. Failing here (graph.ErrNotFound: the org doesn't exist yet) leaves nothing created, so the next
// call for this subject starts clean.
func EnsureUser(ctx context.Context, g *graph.Graph, subject string) error {
	if subject == "" {
		return nil
	}
	key := access.UserKey(subject)
	exists := func() (bool, error) {
		_, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, key)
		if errors.Is(err, graph.ErrNotFound) {
			return false, nil
		}
		return err == nil, err
	}
	if ok, err := exists(); ok || err != nil {
		return err
	}
	// one creation at a time in this process: the web fires several calls at once right after signing in,
	// and two commits creating the same key must not race
	ensureMu.Lock()
	defer ensureMu.Unlock()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if ok, xerr := exists(); ok || xerr != nil {
			return xerr
		}
		if err = createUser(ctx, g, subject); !errors.Is(err, graph.ErrConflict) {
			return err
		}
		// another change moved main meanwhile, or another process created the user: read again
	}
	return err
}

var ensureMu sync.Mutex

// createUser commits the User node of a subject on main, member_of the unit new users join, as a change of
// its own (ADR 0042: every modification is a change, so a new user is journaled and moves the head of main —
// the snapshot pkg/access reads roles and units from sees them at once, which a write by import did not).
func createUser(ctx context.Context, g *graph.Graph, subject string) error {
	org, err := NewUserUnit(ctx, g)
	if err != nil {
		return err
	}
	existing, err := g.NodesOfType(ctx, mcp.NamespaceOrganisation, access.NodeTypeUser)
	if err != nil {
		return err
	}
	u := access.User{Subject: subject}
	key := access.UserKey(subject)
	user := linkTo(createNode(key, access.NodeTypeUser, u.Props()), access.LinkMemberOf, org.Ref())
	user.Rationale = "First sign-in of " + subject
	edits := []graph.NodeEdit{user}
	if len(existing) == 0 {
		// the first user: grant admin through a platform Assignment created in the same commit (ToKey
		// resolves to the User node above), rather than the legacy User.Admin flag (ADR 0047)
		asg := access.Assignment{Roles: []string{access.RoleAdmin}, Description: "First user becomes administrator"}
		edits = append(edits, graph.NodeEdit{
			Key: access.PlatformAssignmentKey(key), Type: access.NodeTypeAssignment, Props: asg.Props(),
			Rationale: "First user becomes administrator",
			Links:     []graph.LinkEdit{{Type: access.LinkAssignsOrg, ToKey: key}},
		})
	}
	return applyOn(ctx, g, mcp.NamespaceOrganisation, "User "+subject, edits)
}

// NewUserUnit returns the organisational unit new users join (ADR 0042): the waiting unit, an OrgUnit an
// administrator created and flagged `waiting` (access.PropWaitingUnit) at their discretion, or
// domain.DefaultOrg when none is flagged. Should several units carry the flag (two concurrent changes each moving it), the
// smallest key wins, so the answer stays deterministic until someone clears the extra one. Read against live
// state, like the rest of EnsureUser: a flag moved by a change is seen at once.
func NewUserUnit(ctx context.Context, g *graph.Graph) (domain.Node, error) {
	units, err := g.NodesOfType(ctx, mcp.NamespaceOrganisation, mcp.NodeTypeOrgUnit)
	if err != nil {
		return domain.Node{}, err
	}
	var found *domain.Node
	for i := range units {
		if access.IsWaitingUnit(units[i].Properties) && (found == nil || units[i].Key < found.Key) {
			found = &units[i]
		}
	}
	if found != nil {
		return *found, nil
	}
	return g.NodeByKey(ctx, mcp.NamespaceOrganisation, domain.DefaultOrg)
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
