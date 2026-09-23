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
//	hasRole(r.sub, "approver") && r.sub.Org == r.obj.Org && r.sub.Subject != r.obj.Owner
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

// DefaultPolicies seeds an empty policy store.
var DefaultPolicies = []Policy{
	{Rule: `hasRole(r.sub, "admin")`, Resource: "*", Action: "*", Effect: "allow"},
	// read access within the organization of the resource (multi-tenant isolation)
	{Rule: `!isAnonymous(r.sub) && (r.obj.Org == "" || r.obj.Org == r.sub.Org)`, Resource: "*", Action: "read", Effect: "allow"},
	{Rule: `hasAnyRole(r.sub, "contributor", "methodologist", "approver") && r.obj.Org == r.sub.Org`, Resource: "process", Action: "*", Effect: "allow"},
	{Rule: `hasRole(r.sub, "methodologist") && r.obj.Org == r.sub.Org`, Resource: "methodology", Action: "*", Effect: "allow"},
	{Rule: `hasRole(r.sub, "methodologist") && r.obj.Org == r.sub.Org`, Resource: "trigger", Action: "fire", Effect: "allow"},
	// four-eyes principle: an approver applies changes of its organization, never its own
	{Rule: `hasRole(r.sub, "approver") && r.sub.Org == r.obj.Org && r.sub.Subject != r.obj.Owner`, Resource: "change", Action: "apply", Effect: "allow"},
}

// Casbin is an Authorizer backed by a Casbin enforcer.
type Casbin struct {
	mu sync.RWMutex
	e  *casbin.Enforcer
}

// NewCasbin creates an enforcer. With a nil adapter, policies live in memory.
// When the policy store is empty it is seeded with DefaultPolicies.
func NewCasbin(adapter persist.Adapter) (*Casbin, error) {
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
	e.AddFunction("isAnonymous", func(args ...any) (any, error) {
		p, _ := args[0].(Principal)
		return p.Anonymous(), nil
	})
	c := &Casbin{e: e}
	if pols, _ := c.Policies(); len(pols) == 0 {
		for _, p := range DefaultPolicies {
			if err := c.AddPolicy(p); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
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
	probe, err := NewCasbin(nil)
	if err != nil {
		return err
	}
	probe.mu.Lock()
	probe.e.ClearPolicy()
	probe.mu.Unlock()
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
