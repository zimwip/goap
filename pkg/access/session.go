package access

import (
	"context"
	"errors"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
)

// Session is what the web learns of the caller once, at sign-in and on each project switch (GET /api/whoami, ADR
// 0070): the principal the platform sees, and the knowledge derived from the identity and the organisation that a
// client would otherwise mirror: the names of the organisation, how its keys are built, what the caller may attempt.
// The principal's fields are those of the JSON object, the rest are added to them.
type Session struct {
	authz.Principal
	// Can are hints for the UI to show or hide what it offers; the server enforces each call anyway.
	Can Capabilities `json:"can"`
	// Structures are the organisation and the projects (ADR 0054): their node types, parent links, roots.
	Structures domain.Structures `json:"structures"`
	Names      Names             `json:"names"`
	// PlatformRoles are the built-in roles an Assignment grants platform-wide (BuiltinRoles).
	PlatformRoles []Role `json:"platformRoles"`
}

// Capabilities are what the caller may attempt, derived by the authorizer (never from a role name).
type Capabilities struct {
	// Administer: what the compiled-in floor allows administrators (the organisation, projects, policies, adapters,
	// methodologies, domains, the model catalogue and quotas, the usage of the whole platform): the controls that only
	// administrators may use.
	Administer bool `json:"administer"`
	// Approve: the caller works on their active project (a platform role, or a role an Assignment grants them on it or a
	// project above), so an approval of someone else's run may be theirs to give. The engine decides, run by run
	// (step:approve, action:run), so this only says whether to tell them of the approvals waiting.
	Approve bool `json:"approve"`
}

// Names are the namespaces, node types, link types, key schemes and properties of the built-in domains the clients
// build and read nodes with: the one place they are written (the units and projects come from the structures).
type Names struct {
	Namespaces struct {
		Organisation string `json:"organisation"`
		Platform     string `json:"platform"`
		// Meta is the namespace of the meta-domain (the methodology elements), left out of the views of what is worked on.
		Meta string `json:"meta"`
	} `json:"namespaces"`
	Types struct {
		OrgUnit     string `json:"orgUnit"`
		ProjectUnit string `json:"projectUnit"`
		User        string `json:"user"`
		Assignment  string `json:"assignment"`
		Adapter     string `json:"adapter"`
		Policy      string `json:"policy"`
		MCP         string `json:"mcp"`
	} `json:"types"`
	Links struct {
		PartOf         string `json:"partOf"`
		ProjectPartOf  string `json:"projectPartOf"`
		MemberOf       string `json:"memberOf"`
		AssignsOrg     string `json:"assignsOrg"`
		AssignsProject string `json:"assignsProject"`
	} `json:"links"`
	Keys struct {
		// User prefixes the key of a user node (UserKey): <prefix><subject>.
		User string `json:"user"`
		// Assignment prefixes the key of an Assignment (AssignmentKey): <prefix><unit>/<project>, the project being
		// PlatformScope for a platform-wide one.
		Assignment    string `json:"assignment"`
		PlatformScope string `json:"platformScope"`
		// Policy prefixes the key of a Policy node (PolicyKey; a client may add its own suffix).
		Policy string `json:"policy"`
	} `json:"keys"`
	Roles struct {
		// Admin is the platform role of an administrator (RoleAdmin).
		Admin string `json:"admin"`
	} `json:"roles"`
	Props struct {
		// Waiting flags the unit new users join (PropWaitingUnit).
		Waiting string `json:"waiting"`
	} `json:"props"`
}

// NamesOf are the names of the built-in domains, the units and projects being those of the structures st.
func NamesOf(st domain.Structures) Names {
	var n Names
	n.Namespaces.Organisation = NamespaceOrganisation
	n.Namespaces.Platform = domain.NamespacePlatform
	n.Namespaces.Meta = domain.NamespaceMethodology
	org, proj := st.Organisation(), st.Project()
	n.Types.OrgUnit, n.Types.ProjectUnit = org.Type, proj.Type
	n.Types.User, n.Types.Assignment = NodeTypeUser, NodeTypeAssignment
	n.Types.Adapter, n.Types.Policy, n.Types.MCP = domain.TypeAdapter, NodeTypePolicy, domain.TypeMCP
	n.Links.PartOf, n.Links.ProjectPartOf = org.Parent, proj.Parent
	n.Links.MemberOf, n.Links.AssignsOrg, n.Links.AssignsProject = LinkMemberOf, LinkAssignsOrg, LinkAssignsProject
	n.Keys.User, n.Keys.Assignment, n.Keys.PlatformScope, n.Keys.Policy = UserPrefix, AssignmentPrefix, PlatformScope, PolicyPrefix
	n.Roles.Admin = RoleAdmin
	n.Props.Waiting = PropWaitingUnit
	return n
}

// ErrNoSession says the organisation cannot be read, so the session cannot be derived yet.
var ErrNoSession = errors.New("session: the organisation cannot be read")

// Session derives the session of a caller from the current snapshot: the principal completed with its roles, what it
// may attempt, the structures and names. The graph being unreadable is an error rather than a session with no
// structures a client could not use.
func (a *Authorizer) Session(ctx context.Context, p authz.Principal) (Session, error) {
	snap, err := a.Dir.Snapshot(ctx)
	if err != nil || snap == nil {
		return Session{}, errors.Join(ErrNoSession, err)
	}
	p = snap.Enrich(p)
	s := Session{Principal: p, Structures: snap.Structures(), Names: NamesOf(snap.Structures()), PlatformRoles: BuiltinRoles()}
	if p.Anonymous() {
		return s, nil
	}
	admin, err := a.Floor().Authorize(ctx, authz.Request{Subject: p, Action: "admin", Resource: authz.Resource{Type: "platform"}})
	if err != nil {
		return Session{}, err
	}
	s.Can.Administer = admin
	project := p.Project
	if project == "" {
		project = s.Structures.Project().Root
	}
	s.Can.Approve = snap.MayAccessProject(p, project)
	return s, nil
}

// Structures are the organisation and the projects the snapshot was read with.
func (s *Snapshot) Structures() domain.Structures { return s.structures }
