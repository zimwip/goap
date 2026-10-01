package authz

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"
)

// Model is the Casbin ABAC model. A policy line is
//
//	p, <subject rule>, <resource type | *>, <action | *>, <allow | deny>
//
// where the subject rule is an expression over r.sub (Principal), r.obj
// (Resource) and r.act, e.g.
//
//	hasRole(r.sub, "tech_lead") && r.sub.Subject != r.obj.Owner
//
// Functions: hasRole(sub, role), hasAnyRole(sub, role...), hasRoleIn(sub, role, obj), onProject(sub) (holds a
// role on the resource's project), mayRun(sub, obj) (holds one of obj.Roles, or any role when it lists none),
// isAnonymous(sub).
//
// A request is allowed when at least one allow rule matches and no deny rule does.
const Model = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub_rule, obj_type, act, eft

[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))

[matchers]
m = (p.obj_type == "*" || r.obj.Type == p.obj_type) && (p.act == "*" || r.act == p.act) && eval(p.sub_rule)
`

// Policy is one ABAC rule.
type Policy struct {
	Rule     string `json:"rule"`
	Resource string `json:"resource"`
	Action   string `json:"action"`
	Effect   string `json:"effect"` // allow | deny
}

func (p Policy) params() []any { return []any{p.Rule, p.Resource, p.Action, p.Effect} }

// DefaultPolicies seeds an empty policy store (ADR 0043). A user holds no role of their own: an administrator
// (the User's admin flag, RoleAdmin) may do everything; anyone else acts on a project with the roles their
// Assignments grant there, which the authorizer merges into the principal for the resource's project. Holding
// any role on the project (onProject) lets them work on it; what they write goes through the steps, agents and
// actions their roles may run (step perform/approve, action run). The platform itself (methodologies, domains,
// triggers, organisation, policies, adapters) is administered by administrators.
var DefaultPolicies = []Policy{
	{Rule: `hasRole(r.sub, "admin")`, Resource: "*", Action: "*", Effect: "allow"},
	// a platform-wide reader (ADR 0046, granted by an Assignment naming no project) reads everything, past
	// the organization/project scoping below
	{Rule: `hasRole(r.sub, "reader")`, Resource: "*", Action: "read", Effect: "allow"},
	// read access within the organization of the resource (multi-tenant isolation), or on a project one works on
	{Rule: `!isAnonymous(r.sub) && (r.obj.Org == "" || r.obj.Org == r.sub.Org || onProject(r.sub))`, Resource: "*", Action: "read", Effect: "allow"},
	// processes, data objects, lifecycle transitions (ADR 0014) and tools (ADR 0019) of a project: its members
	{Rule: `onProject(r.sub)`, Resource: "process", Action: "*", Effect: "allow"},
	{Rule: `onProject(r.sub)`, Resource: "object", Action: "create", Effect: "allow"},
	{Rule: `onProject(r.sub)`, Resource: "node", Action: "transition", Effect: "allow"},
	{Rule: `onProject(r.sub)`, Resource: "tool", Action: "call", Effect: "allow"},
	// four-eyes principle: a member of the project applies its changes, never their own
	{Rule: `onProject(r.sub) && r.sub.Subject != r.obj.Owner`, Resource: "change", Action: "apply", Effect: "allow"},
	// production deployments: a release manager of the project, never on its own change
	{Rule: `hasRole(r.sub, "release_manager") && r.sub.Subject != r.obj.Owner`, Resource: "release", Action: "deploy", Effect: "allow"},
	// steps of a process (ADR 0035 §2): whoever holds the responsible role on the project carries them out;
	// whoever holds the accountable role approves them, never on its own change
	{Rule: `hasRoleIn(r.sub, r.obj.Role, r.obj)`, Resource: "step", Action: "perform", Effect: "allow"},
	{Rule: `hasRoleIn(r.sub, r.obj.Accountable, r.obj) && r.sub.Subject != r.obj.Owner`, Resource: "step", Action: "approve", Effect: "allow"},
	// agents and actions (ADR 0043): one of the roles they declare (r.obj.Roles), any member of the project
	// when they declare none
	{Rule: `mayRun(r.sub, r.obj)`, Resource: "action", Action: "run", Effect: "allow"},
	{Rule: `mayRun(r.sub, r.obj)`, Resource: "agent", Action: "run", Effect: "allow"},
	// a model of the catalog restricted to some roles (ADR 0021): one of them, held on the caller's project
	{Rule: `mayRun(r.sub, r.obj)`, Resource: "model", Action: "use", Effect: "allow"},
}

// LegacyDefaultPolicies are the default policies before ADR 0043, when users held roles of their own
// (contributor, methodologist, approver...): a graph still holding them unchanged gets the current defaults
// instead (graphsvc.SeedAccess).
var LegacyDefaultPolicies = []Policy{
	{Rule: `hasRole(r.sub, "admin")`, Resource: "*", Action: "*", Effect: "allow"},
	// read access within the organization of the resource (multi-tenant isolation)
	{Rule: `!isAnonymous(r.sub) && (r.obj.Org == "" || r.obj.Org == r.sub.Org)`, Resource: "*", Action: "read", Effect: "allow"},
	{Rule: `hasAnyRole(r.sub, "contributor", "methodologist", "approver") && r.obj.Org == r.sub.Org`, Resource: "process", Action: "*", Effect: "allow"},
	{Rule: `hasRole(r.sub, "methodologist") && r.obj.Org == r.sub.Org`, Resource: "methodology", Action: "*", Effect: "allow"},
	// domains (node and link types, lifecycles, algorithms), edited independently of methodologies
	{Rule: `hasRole(r.sub, "methodologist") && r.obj.Org == r.sub.Org`, Resource: "domain", Action: "*", Effect: "allow"},
	// data objects created on the graph (typed by a node type): same roles as processes
	{Rule: `hasAnyRole(r.sub, "contributor", "methodologist", "approver") && r.obj.Org == r.sub.Org`, Resource: "object", Action: "create", Effect: "allow"},
	{Rule: `hasRole(r.sub, "methodologist") && r.obj.Org == r.sub.Org`, Resource: "trigger", Action: "fire", Effect: "allow"},
	// lifecycle transitions of nodes (ADR 0014); a transition may require another permission
	{Rule: `hasAnyRole(r.sub, "contributor", "methodologist", "approver") && r.obj.Org == r.sub.Org`, Resource: "node", Action: "transition", Effect: "allow"},
	// tools of the MCPs bound by the organization of the change (ADR 0019)
	{Rule: `hasAnyRole(r.sub, "contributor", "methodologist", "approver") && r.obj.Org == r.sub.Org`, Resource: "tool", Action: "call", Effect: "allow"},
	// four-eyes principle: an approver applies changes of its organization, never its own
	{Rule: `hasRole(r.sub, "approver") && r.sub.Org == r.obj.Org && r.sub.Subject != r.obj.Owner`, Resource: "change", Action: "apply", Effect: "allow"},
	// production deployments: a release manager of the organization, never on its own change
	{Rule: `hasRole(r.sub, "release_manager") && r.sub.Org == r.obj.Org && r.sub.Subject != r.obj.Owner`, Resource: "release", Action: "deploy", Effect: "allow"},
	// steps of a process (ADR 0035 §2): whoever holds the responsible role in the unit holding the change (or above
	// it) carries them out; whoever holds the accountable role approves them, never on its own change
	{Rule: `hasRoleIn(r.sub, r.obj.Role, r.obj)`, Resource: "step", Action: "perform", Effect: "allow"},
	{Rule: `hasRoleIn(r.sub, r.obj.Accountable, r.obj) && r.sub.Subject != r.obj.Owner`, Resource: "step", Action: "approve", Effect: "allow"},
}

// FloorPolicies are the rules that hold whatever the stored policies say, so that a faulty
// policy can never lock the administrators out.
var FloorPolicies = []Policy{DefaultPolicies[0]}

// Casbin is an Authorizer backed by a Casbin enforcer.
type Casbin struct {
	mu sync.RWMutex
	e  *casbin.Enforcer
}

// NewCasbin creates an enforcer. With a nil adapter, policies live in memory.
// When the policy store is empty it is seeded with DefaultPolicies.
func NewCasbin(adapter persist.Adapter) (*Casbin, error) {
	c, err := newCasbin(adapter)
	if err != nil {
		return nil, err
	}
	if pols, _ := c.Policies(); len(pols) == 0 {
		for _, p := range DefaultPolicies {
			if err := c.AddPolicy(p); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}

// NewCasbinWith creates an in-memory enforcer holding exactly the given policies.
func NewCasbinWith(policies []Policy) (*Casbin, error) {
	c, err := newCasbin(nil)
	if err != nil {
		return nil, err
	}
	for _, p := range policies {
		if err := c.AddPolicy(p); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func newCasbin(adapter persist.Adapter) (*Casbin, error) {
	m, err := model.NewModelFromString(Model)
	if err != nil {
		return nil, err
	}
	var e *casbin.Enforcer
	if adapter == nil {
		e, err = casbin.NewEnforcer(m)
	} else {
		e, err = casbin.NewEnforcer(m, adapter)
	}
	if err != nil {
		return nil, err
	}
	e.AddFunction("hasRole", func(args ...any) (any, error) {
		if len(args) != 2 {
			return false, fmt.Errorf("hasRole(sub, role)")
		}
		p, _ := args[0].(Principal)
		role, _ := args[1].(string)
		return slices.Contains(p.Roles, role), nil
	})
	e.AddFunction("hasAnyRole", func(args ...any) (any, error) {
		if len(args) < 2 {
			return false, fmt.Errorf("hasAnyRole(sub, role...)")
		}
		p, _ := args[0].(Principal)
		for _, a := range args[1:] {
			if r, _ := a.(string); slices.Contains(p.Roles, r) {
				return true, nil
			}
		}
		return false, nil
	})
	e.AddFunction("hasRoleIn", func(args ...any) (any, error) {
		if len(args) != 3 {
			return false, fmt.Errorf("hasRoleIn(sub, role, obj)")
		}
		p, _ := args[0].(Principal)
		role, _ := args[1].(string)
		res, _ := args[2].(Resource)
		return p.HasRoleIn(role, res), nil
	})
	e.AddFunction("onProject", func(args ...any) (any, error) {
		if len(args) != 1 {
			return false, fmt.Errorf("onProject(sub)")
		}
		p, _ := args[0].(Principal)
		return p.OnProject(), nil
	})
	e.AddFunction("mayRun", func(args ...any) (any, error) {
		if len(args) != 2 {
			return false, fmt.Errorf("mayRun(sub, obj)")
		}
		p, _ := args[0].(Principal)
		res, _ := args[1].(Resource)
		return p.MayRun(res), nil
	})
	e.AddFunction("isAnonymous", func(args ...any) (any, error) {
		p, _ := args[0].(Principal)
		return p.Anonymous(), nil
	})
	return &Casbin{e: e}, nil
}

// Authorize implements Authorizer. Anonymous principals are always denied.
func (c *Casbin) Authorize(_ context.Context, req Request) (bool, error) {
	if req.Subject.Anonymous() {
		return false, nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.e.Enforce(req.Subject, req.Resource, req.Action)
}

// Policies lists the rules.
func (c *Casbin) Policies() ([]Policy, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rules, err := c.e.GetPolicy()
	if err != nil {
		return nil, err
	}
	out := make([]Policy, 0, len(rules))
	for _, r := range rules {
		if len(r) == 4 {
			out = append(out, Policy{Rule: r[0], Resource: r[1], Action: r[2], Effect: r[3]})
		}
	}
	return out, nil
}

// Validate checks a policy by evaluating it once against a probe request.
func Validate(p Policy) error {
	if p.Effect != "allow" && p.Effect != "deny" {
		return fmt.Errorf("effect must be allow or deny")
	}
	if p.Rule == "" || p.Resource == "" || p.Action == "" {
		return fmt.Errorf("rule, resource and action are required")
	}
	probe, err := NewCasbinWith(nil)
	if err != nil {
		return err
	}
	if err := probe.AddPolicy(p); err != nil {
		return err
	}
	res := p.Resource
	if res == "*" {
		res = "probe"
	}
	act := p.Action
	if act == "*" {
		act = "probe"
	}
	_, err = probe.Authorize(context.Background(), Request{Subject: Principal{Subject: "probe"}, Action: act, Resource: Resource{Type: res}})
	if err != nil {
		return fmt.Errorf("invalid rule: %w", err)
	}
	return nil
}

// AddPolicy adds a rule (persisted by the adapter).
func (c *Casbin) AddPolicy(p Policy) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.e.AddPolicy(p.params()...)
	return err
}

// RemovePolicy removes a rule.
func (c *Casbin) RemovePolicy(p Policy) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.e.RemovePolicy(p.params()...)
	return err
}

// Reload reloads the rules from the adapter (other replicas' changes).
func (c *Casbin) Reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.e.LoadPolicy()
}
