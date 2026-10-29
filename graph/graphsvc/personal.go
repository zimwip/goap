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

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/pbconv"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/rpcerr"
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
		return access.PersonalUnit(me), rpcerr.ToConnect(EnsureUser(ctx, h.Graph, me))
	case access.IsPersonalUnit(owner) && access.PersonalSubject(owner) != subjectOf(ctx):
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
// The default org is resolved *before* the node is created: the bootstrap (Boot) can still be running at startup (it
// waits on the registry to publish the type catalogue), and a caller seen in that window must not leave a
// permanently broken User behind — one with no member_of and no admin, since a node once created would make
// every later subject see the namespace as "already has a User" and never get the first-admin bootstrap
// either. Failing here (graph.ErrNotFound: the org doesn't exist yet) leaves nothing created, so the next
// call for this subject starts clean.
func EnsureUser(ctx context.Context, g *graph.Graph, subject string) error {
	// a platform service (system:registry, system:trigger:<key>, ...) is no person: it never becomes a User,
	// so it can never take the first-administrator grant below
	if subject == "" || (authz.Principal{Subject: subject}).System() {
		return nil
	}
	key := access.UserKey(subject)
	exists := func() (bool, error) {
		_, err := g.NodeByKey(ctx, access.NamespaceOrganisation, key)
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
	// the creation writes, whatever the request that met the caller
	ctx = platform.Writing(ctx)
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
	existing, err := g.ViewNodes(ctx, orgMain, graph.NodeQuery{Type: access.NodeTypeUser, Limit: 1})
	if err != nil {
		return err
	}
	u := access.User{Subject: subject}
	key := access.UserKey(subject)
	user := linkTo(SeedNode(key, access.NodeTypeUser, u.Props()), access.LinkMemberOf, org.Ref())
	user.Rationale = "First sign-in of " + subject
	// activate it in the same commit (ADR 0048's user lifecycle starts a new account "proposed"): a sign-in
	// commits it usable right away, with no separate admin step needed for every single new user.
	user.State = "active"
	edits := []graph.NodeEdit{user}
	if existing.Total == 0 {
		// the first user: grant admin through a platform Assignment created in the same commit (ToKey
		// resolves to the User node above, ADR 0047)
		asg := access.Assignment{Roles: []string{access.RoleAdmin}, Description: "First user becomes administrator"}
		edits = append(edits, graph.NodeEdit{
			Key: access.PlatformAssignmentKey(key), Type: access.NodeTypeAssignment, Props: asg.Props(),
			Rationale: "First user becomes administrator",
			Links:     []graph.LinkEdit{{Type: access.LinkAssignsOrg, ToKey: key}},
		})
	}
	// the activating transition (and, for the first user, the admin grant) is internal bookkeeping of the
	// sign-in flow, not an action the caller is themselves authorized for: a brand-new subject holds no role
	// or project yet, so running this under their own identity would make TransitionAuthorizer deny its own
	// activation for every single new user. It runs as the system principal (ADR 0048, lifecycle.go's
	// TransitionAuthorizer).
	return SeedChange(System(ctx), g, access.NamespaceOrganisation, "User "+subject, edits)
}

// orgMain is the head of main of the organisation, the view the sign-in bookkeeping reads (ADR 0157): what the last
// change applied there wrote.
var orgMain = graph.View{Namespace: access.NamespaceOrganisation, Branch: domain.MainBranch}

// NewUserUnit returns the organisational unit new users join (ADR 0042): the waiting unit, an OrgUnit an
// administrator created and flagged `waiting` (access.PropWaitingUnit) at their discretion, or
// g.Structure(domain.StructureOrganisation).Root when none is flagged. Should several units carry the flag (two concurrent changes each moving it), the
// smallest key wins, so the answer stays deterministic until someone clears the extra one. Read against live
// state, like the rest of EnsureUser: a flag moved by a change is seen at once.
func NewUserUnit(ctx context.Context, g *graph.Graph) (domain.Node, error) {
	units, err := g.ViewNodesOfType(ctx, orgMain, access.NodeTypeOrgUnit)
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
	return g.NodeByKey(ctx, access.NamespaceOrganisation, g.Structure(domain.StructureOrganisation).Root)
}

// SystemPrincipal is the identity of the graph service acting by itself (seeds, the bookkeeping of a sign-in):
// a named service, never an anonymous caller, which the transition authorizers refuse.
var SystemPrincipal = authz.System("graph", access.RoleAdmin)

// System returns ctx acting as the graph service itself.
func System(ctx context.Context) context.Context { return authz.With(ctx, SystemPrincipal) }

// Identify is the interceptor that identifies the caller of every RPC: it applies the identity of the request
// to the context the handler receives (not only in the methods that call Identity.Context themselves) and
// refuses an anonymous request, so no call reaches the graph without a principal. First of the chain.
func (h *Handler) Identify() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			who := h.Identity.Context(ctx, req.Header())
			if authz.From(who).Anonymous() {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("the caller is not identified"))
			}
			return next(who, req)
		}
	})
}

// Positions is the interceptor that carries the positions of the logs of the changes (ADR 0153): the reads of a request
// must reach the ones its graphapi.HeaderAtLeast names, and its answer names in graphapi.HeaderChangeSeq the positions
// its writes left the logs at, so that the caller can require them of its next reads.
func (h *Handler) Positions() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			for id, seq := range graphapi.ParsePositions(req.Header().Get(graphapi.HeaderAtLeast)) {
				ctx = graphapi.AtLeast(ctx, id, seq)
			}
			ctx, w := graphapi.WithWatermark(ctx)
			res, err := next(ctx, req)
			if m := w.All(); len(m) > 0 {
				if res != nil {
					res.Header().Set(graphapi.HeaderChangeSeq, graphapi.FormatPositions(m))
				} else if ce := new(connect.Error); errors.As(err, &ce) {
					ce.Meta().Set(graphapi.HeaderChangeSeq, graphapi.FormatPositions(m))
				}
			}
			return res, err
		}
	})
}

// EnsureCaller is the interceptor that calls EnsureUser for every authenticated caller (ADR 0039),
// deduplicated per process: a subject already seen is not checked again, so this costs one NodeByKey (and,
// the first time only, one write) per subject per process lifetime, not per call. A content graph (Space set) holds no
// organisation (ADR 0154 §9): the users are the platform graph's, so it declares none; trying would fail at every call
// and run each one behind ensureMu and the reads on the writer.
func (h *Handler) EnsureCaller() connect.Interceptor {
	var seen sync.Map // subject -> struct{}
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			who := h.Identity.Context(ctx, req.Header())
			if subject := subjectOf(who); subject != "" && h.Space == "" {
				if _, ok := seen.Load(subject); !ok {
					if err := EnsureUser(who, h.Graph, subject); err == nil {
						seen.Store(subject, struct{}{})
					}
				}
			}
			return next(who, req)
		}
	})
}

// PersonalScope is the interceptor that keeps a change to those who may read it (Handler.mayRead): a request naming a
// change (change_id, or id for GetChange and UpdateChange) that is personal to someone else, or of a project the caller
// does not work on (ADR 0138), is answered as not found.
func (h *Handler) PersonalScope() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			who := h.Identity.Context(ctx, req.Header())
			if msg, ok := req.Any().(proto.Message); ok {
				ids := changeIDsIn(req.Spec().Procedure, msg)
				if h.locksWrites.Load() && lockedWrite(req.Spec().Procedure, ids) {
					return next(who, req) // checked by LockedWrites, in the transaction of the write
				}
				for _, id := range ids {
					// the checks before the operation (the operation authorizer, the handler's) read the same head
					who = graph.WithHeadMemo(who)
					if c, err := h.Graph.ChangeHead(who, id); err == nil && !h.mayRead(who, c) {
						return nil, denyPersonal(id)
					}
				}
			}
			return next(who, req)
		}
	})
}

// LockedWrites is the interceptor running the edits of the drafts of one change (lockedWrites) whole in one transaction
// of the graph holding the change's lock from the start (Graph.InChangeLock): the header of the change is read once,
// for the check of PersonalScope (which leaves them to it), the handler's and the operation's. It goes after
// EnsureCaller, whose write is a transaction of its own.
func (h *Handler) LockedWrites() connect.Interceptor {
	h.locksWrites.Store(true)
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			msg, ok := req.Any().(proto.Message)
			if !ok {
				return next(ctx, req)
			}
			ids := changeIDsIn(req.Spec().Procedure, msg)
			if !lockedWrite(req.Spec().Procedure, ids) {
				return next(ctx, req)
			}
			who := graph.WithHeadMemo(h.Identity.Context(ctx, req.Header()))
			var res connect.AnyResponse
			err := h.Graph.InChangeLock(who, ids[0], func(who context.Context) error {
				if c, err := h.Graph.ChangeHead(who, ids[0]); err == nil && !h.mayRead(who, c) {
					return denyPersonal(ids[0])
				}
				var err error
				res, err = next(who, req)
				return err
			})
			return res, err
		}
	})
}

func lockedWrite(procedure string, ids []domain.ChangeID) bool {
	return len(ids) == 1 && lockedWrites[procedure[strings.LastIndexByte(procedure, '/')+1:]]
}

// lockedWrites are the procedures writing one change in one transaction of the graph (the edits of its drafts, a review
// batch, its purge, its commit, merge and close: a rebase they leave with conflicts is kept, the transaction committed,
// then refused), run whole in the
// transaction holding the change's lock (Graph.InChangeLock).
var lockedWrites = map[string]bool{
	"ImpactNodeCreate": true, "ImpactNodeUpdate": true, "ImpactNodeCheckout": true,
	"ImpactLinkCreate": true, "ImpactLinkUpdate": true, "ImpactLinkDelete": true, "ImpactNodeReviewBatch": true,
	"DeleteChange": true, "CommitChange": true, "MergeChange": true, "CloseChange": true,
}

// changeIDsIn returns the changes a request names: its change_id (id for GetChange and UpdateChange) and the change of
// the view it reads (ADR 0157).
func changeIDsIn(procedure string, msg proto.Message) []domain.ChangeID {
	m := msg.ProtoReflect()
	name := protoreflect.Name("change_id")
	if strings.HasSuffix(procedure, "/GetChange") || strings.HasSuffix(procedure, "/UpdateChange") {
		name = "id"
	}
	var out []domain.ChangeID
	if id := stringField(m, name); id != "" {
		out = append(out, domain.ChangeID(id))
	}
	if v := viewField(m); v != nil {
		if id := stringField(v, "change_id"); id != "" {
			out = append(out, domain.ChangeID(id))
		}
	}
	return out
}

// visibleTo reports whether the caller may see a change: every change but the personal ones of others.
func visibleTo(ctx context.Context, c domain.Change) bool {
	return !access.IsPersonal(c) || access.IsPersonalTo(c, subjectOf(ctx))
}

func (h *Handler) DeleteChange(ctx context.Context, r *connect.Request[graphv1.DeleteChangeRequest]) (*connect.Response[graphv1.DeleteChangeResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	c, err := h.Graph.PurgeChange(ctx, domain.ChangeID(r.Msg.ChangeId))
	if err == nil {
		c.Items, c.Nodes = nil, nil
	}
	return res(&graphv1.DeleteChangeResponse{Change: pbconv.ChangeToPB(c)}, err)
}
