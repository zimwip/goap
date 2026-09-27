// Package access holds who may do what as graph data (organisation namespace): User nodes
// (profile, roles, membership of a unit through member_of) and Policy nodes (the ABAC rules).
// An Authorizer built on a Directory evaluates them; the compiled-in floor keeps administrators
// in whatever the stored policies say.
package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graphsnap"
	"github.com/zimwip/goap/pkg/mcp"
)

// Types and links of the graph objects.
const (
	NodeTypeUser   = "organisation@User"
	NodeTypePolicy = "organisation@Policy"
	LinkMemberOf   = "organisation@member_of"

	// ResourcePolicy is the ABAC resource that guards changes to User and Policy nodes.
	ResourcePolicy = "policy"
)

// User is a person or a service account. Roles are granted to the subject whatever the token carries.
type User struct {
	Subject     string   `json:"subject"`
	DisplayName string   `json:"displayName,omitempty"`
	Email       string   `json:"email,omitempty"`
	Locale      string   `json:"locale,omitempty"`
	Roles       []string `json:"roles,omitempty"`
	// Unit is the organisational unit the user belongs to (the member_of link).
	Unit string `json:"-"`
}

// UserKey is the key of the node of a user.
func UserKey(subject string) string { return "USR:" + subject }

// PolicyKey is the key of the node of a policy: its target plus a digest of the rule.
func PolicyKey(p authz.Policy) string {
	h := sha256.Sum256([]byte(p.Rule + "\x00" + p.Effect))
	return "POL:" + p.Resource + "/" + p.Action + "/" + hex.EncodeToString(h[:4])
}

// Props returns the properties of the User node.
func (u User) Props() map[string]any {
	m := map[string]any{"subject": u.Subject}
	for k, v := range map[string]string{"displayName": u.DisplayName, "email": u.Email, "locale": u.Locale} {
		if v != "" {
			m[k] = v
		}
	}
	if len(u.Roles) > 0 {
		roles := make([]any, len(u.Roles))
		for i, r := range u.Roles {
			roles[i] = r
		}
		m["roles"] = roles
	}
	return m
}

func str(props map[string]any, k string) string {
	s, _ := props[k].(string)
	return s
}

// UserFromProps reads a user from the properties of its node.
func UserFromProps(props map[string]any) (User, error) {
	u := User{Subject: str(props, "subject"), DisplayName: str(props, "displayName"), Email: str(props, "email"), Locale: str(props, "locale")}
	if u.Subject == "" {
		return u, errors.New("user without subject")
	}
	switch r := props["roles"].(type) {
	case nil:
	case []any:
		for _, x := range r {
			if s, ok := x.(string); ok && s != "" {
				u.Roles = append(u.Roles, s)
			}
		}
	case []string:
		u.Roles = slices.Clone(r)
	default:
		return u, fmt.Errorf("user %s: roles must be a list of strings", u.Subject)
	}
	return u, nil
}

// PolicyProps returns the properties of the Policy node.
func PolicyProps(p authz.Policy) map[string]any {
	return map[string]any{"rule": p.Rule, "resource": p.Resource, "action": p.Action, "effect": p.Effect}
}

// PolicyFromProps reads a policy from the properties of its node and validates it.
func PolicyFromProps(props map[string]any) (authz.Policy, error) {
	p := authz.Policy{Rule: str(props, "rule"), Resource: str(props, "resource"), Action: str(props, "action"), Effect: str(props, "effect")}
	return p, authz.Validate(p)
}

// Graph is the part of the graph the directory reads.
type Graph interface {
	BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
}

// Snapshot is the users and policies as of one baseline.
type Snapshot struct {
	Baseline domain.BaselineID
	// Problems lists the nodes that could not be read.
	Problems []string
	Policies []authz.Policy

	users map[string]User
}

// BuildSnapshot reads the users and policies of a baseline graph.
func BuildSnapshot(id domain.BaselineID, nodes []domain.Node, links []domain.Link) *Snapshot {
	s := &Snapshot{Baseline: id, users: map[string]User{}}
	byID := map[domain.NodeID]domain.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
		// nodes is scoped to the organisation namespace by the Directory's cache (Namespace:
		// mcp.NamespaceOrganisation); this switch does not need to filter it again.
		switch n.Type {
		case NodeTypeUser:
			u, err := UserFromProps(n.Properties)
			if err != nil {
				s.Problems = append(s.Problems, fmt.Sprintf("%s: %v", n.Key, err))
				continue
			}
			s.users[u.Subject] = u
		case NodeTypePolicy:
			p, err := PolicyFromProps(n.Properties)
			if err != nil {
				s.Problems = append(s.Problems, fmt.Sprintf("%s: %v", n.Key, err))
				continue
			}
			s.Policies = append(s.Policies, p)
		}
	}
	for _, l := range links {
		from, to := byID[l.From.ID], byID[l.To.ID]
		if l.Type == LinkMemberOf && from.Type == NodeTypeUser && to.Type == mcp.NodeTypeOrgUnit {
			if u, ok := s.users[str(from.Properties, "subject")]; ok {
				u.Unit = to.Key
				s.users[u.Subject] = u
			}
		}
	}
	sort.Slice(s.Policies, func(i, j int) bool { return PolicyKey(s.Policies[i]) < PolicyKey(s.Policies[j]) })
	return s
}

// User returns the user of a subject.
func (s *Snapshot) User(subject string) (User, bool) {
	u, ok := s.users[subject]
	return u, ok
}

// Users lists the users by subject.
func (s *Snapshot) Users() []User {
	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out
}

// Enrich completes a principal with what the graph knows of its subject: the roles of its User
// node are added to those of the token, and the unit it belongs to is its organisation when the
// token names none.
func (s *Snapshot) Enrich(p authz.Principal) authz.Principal {
	u, ok := s.users[p.Subject]
	if !ok || p.Anonymous() {
		return p
	}
	roles := slices.Clone(p.Roles)
	for _, r := range u.Roles {
		if !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	p.Roles = roles
	if p.Org == "" {
		p.Org = u.Unit
	}
	return p
}

// Directory reads the snapshot of the head of the main branch (see graphsnap.Cache); TTL is how often the
// head is looked at, one second by default.
type Directory struct {
	Graph Graph
	TTL   time.Duration

	once  sync.Once
	cache graphsnap.Cache[*Snapshot]
}

// Snapshot returns the current snapshot. A graph without any baseline yields an empty one; when the graph
// cannot be read the last snapshot (nil if none) is returned with the error.
func (d *Directory) Snapshot(ctx context.Context) (*Snapshot, error) {
	d.once.Do(func() {
		d.cache = graphsnap.Cache[*Snapshot]{Graph: d.Graph, Namespace: mcp.NamespaceOrganisation, TTL: d.TTL, Build: BuildSnapshot}
	})
	s, _, err := d.cache.Get(ctx)
	return s, err
}

// Enrich completes a principal from the current snapshot; when the graph cannot be read the
// principal is returned as it is.
func (d *Directory) Enrich(ctx context.Context, p authz.Principal) authz.Principal {
	if s, _ := d.Snapshot(ctx); s != nil {
		return s.Enrich(p)
	}
	return p
}

// Authorizer decides access requests from the Policy nodes of the graph, for the principal
// completed with its User node. The floor (administrators may do everything) is checked first and
// holds whatever the policies say. While the graph holds no policy, or cannot be read, the
// compiled-in default policies apply.
type Authorizer struct {
	Dir *Directory

	mu       sync.Mutex
	baseline domain.BaselineID
	casbin   *authz.Casbin
	defaults *authz.Casbin
	floor    *authz.Casbin
}

var _ authz.Authorizer = (*Authorizer)(nil)

// NewAuthorizer returns an authorizer over a directory.
func NewAuthorizer(dir *Directory) (*Authorizer, error) {
	defaults, err := authz.NewCasbinWith(authz.DefaultPolicies)
	if err != nil {
		return nil, err
	}
	floor, err := authz.NewCasbinWith(authz.FloorPolicies)
	if err != nil {
		return nil, err
	}
	return &Authorizer{Dir: dir, defaults: defaults, floor: floor}, nil
}

// Floor is the authorizer of the compiled-in floor alone (for the principal completed with its User
// node); it guards the changes to policies and users.
func (a *Authorizer) Floor() authz.Authorizer { return floorAuthorizer{a} }

type floorAuthorizer struct{ a *Authorizer }

func (f floorAuthorizer) Authorize(ctx context.Context, req authz.Request) (bool, error) {
	if req.Subject.Anonymous() {
		return false, nil
	}
	req.Subject = f.a.Dir.Enrich(ctx, req.Subject)
	return f.a.floor.Authorize(ctx, req)
}

func (a *Authorizer) enforcer(ctx context.Context) (*authz.Casbin, *Snapshot) {
	snap, _ := a.Dir.Snapshot(ctx)
	if snap == nil || len(snap.Policies) == 0 {
		return a.defaults, snap
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.casbin == nil || a.baseline != snap.Baseline {
		c, err := authz.NewCasbinWith(snap.Policies)
		if err != nil {
			return a.defaults, snap
		}
		a.casbin, a.baseline = c, snap.Baseline
	}
	return a.casbin, snap
}

// Authorize implements authz.Authorizer.
func (a *Authorizer) Authorize(ctx context.Context, req authz.Request) (bool, error) {
	if req.Subject.Anonymous() {
		return false, nil
	}
	c, snap := a.enforcer(ctx)
	if snap != nil {
		req.Subject = snap.Enrich(req.Subject)
	}
	if ok, err := a.floor.Authorize(ctx, req); err != nil || ok {
		return ok, err
	}
	return c.Authorize(ctx, req)
}
