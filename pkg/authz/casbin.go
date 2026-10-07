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
// (RoleAdmin, a platform role) may do everything; anyone else acts on a project with the roles their
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
	// approving a requirement (alm): the control is the accepted review of the change, which its guard requires (ADR
	// 0076); the transition itself is a member's
	{Rule: `onProject(r.sub)`, Resource: "requirement", Action: "approve", Effect: "allow"},
	{Rule: `onProject(r.sub)`, Resource: "tool", Action: "call", Effect: "allow"},
	// the lifecycle of a change (ADR 0058): its gates are decisions and conditions, the move itself is a member's
	{Rule: `onProject(r.sub)`, Resource: "change", Action: "transition", Effect: "allow"},
	// moving a change to another project (ADR 0091): asked on both projects, so a member of both
	{Rule: `onProject(r.sub)`, Resource: "change", Action: "move", Effect: "allow"},
	// the prompts and answers of the model calls of a change (its log, stream model) may hold anything the project works
	// on: its members inspect them (a graph holding stored policies grants it to administrators until a rule is added)
	{Rule: `onProject(r.sub)`, Resource: "prompt", Action: "inspect", Effect: "allow"},
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
	// signing a derogation (ADR 0075 §2): whoever holds the role of signatory (granted on the project); an administrator
	// signs through the first rule
	{Rule: `hasRole(r.sub, "derogation_signatory")`, Resource: "derogation", Action: "sign", Effect: "allow"},
	// the level of a change asks its signatory a role (policy of the organisation, ADR 0075 §3): one of r.obj.Roles, held
	// on the project
	{Rule: `mayRun(r.sub, r.obj)`, Resource: "derogation", Action: "sign-role", Effect: "allow"},
	// lowering the criticality of a change is an administrator's unless a policy grants it (ADR 0075 §3): no default
	// rule for change:lower-criticality
	// a model of the catalog restricted to some roles (ADR 0021): one of them, held on the caller's project
	{Rule: `mayRun(r.sub, r.obj)`, Resource: "model", Action: "use", Effect: "allow"},
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
