package authz

import (
	"context"
	"errors"
	"testing"
)

func TestDefaultPolicies(t *testing.T) {
	ctx := context.Background()
	c, err := NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	// a principal's roles are the ones it holds on the resource's project (merged by the authorizer, ADR 0043)
	change := Resource{Type: "change", ID: "c1", Org: "acme", Owner: "carol", ProjectID: "PROJ-A"}
	cases := []struct {
		name string
		sub  Principal
		act  string
		res  Resource
		want bool
	}{
		{"admin", Principal{Subject: "root", Roles: []string{"admin"}}, "apply", change, true},
		{"member of the project", Principal{Subject: "alice", Org: "acme", Roles: []string{"tech_lead"}}, "apply", change, true},
		{"four eyes", Principal{Subject: "carol", Org: "acme", Roles: []string{"tech_lead"}}, "apply", change, false},
		{"member from another org", Principal{Subject: "bob", Org: "globex", Roles: []string{"developer"}}, "apply", change, true},
		{"not on the project", Principal{Subject: "dave", Org: "acme"}, "apply", change, false},
		{"process", Principal{Subject: "dave", Org: "acme", Roles: []string{"developer"}}, "start", Resource{Type: "process", Org: "acme"}, true},
		{"process off the project", Principal{Subject: "dave", Org: "acme"}, "start", Resource{Type: "process", Org: "acme"}, false},
		{"read the project of another org", Principal{Subject: "dave", Org: "acme", Roles: []string{"developer"}}, "read", Resource{Type: "process", Org: "globex"}, true},
		{"a resource of no org is not read by default", Principal{Subject: "eve", Org: "acme"}, "read", Resource{Type: "methodology"}, false},
		{"the nodes of a namespace", Principal{Subject: "eve", Org: "acme"}, "read", Resource{Type: "alm@Requirement", Namespace: "alm"}, true},
		{"a platform reader is no member", Principal{Subject: "rex", Global: []string{"reader"}}, "start", Resource{Type: "process", Org: "acme"}, false},
		{"a platform reader reads", Principal{Subject: "rex", Global: []string{"reader"}}, "read", Resource{Type: "process", Org: "globex"}, true},
		{"a platform administrator", Principal{Subject: "root", Global: []string{"admin"}}, "publish", Resource{Type: "methodology"}, true},
		{"read same org", Principal{Subject: "eve", Org: "acme"}, "read", Resource{Type: "process", Org: "acme"}, true},
		{"read other org", Principal{Subject: "eve", Org: "acme"}, "read", Resource{Type: "process", Org: "globex"}, false},
		{"write methodology", Principal{Subject: "eve", Org: "acme", Roles: []string{"developer"}}, "write", Resource{Type: "methodology"}, false},
		{"methodology: administrators only", Principal{Subject: "mia", Org: "acme", Roles: []string{"methodologist"}}, "publish", Resource{Type: "methodology", Org: "acme"}, false},
		{"an action declaring no role is closed", Principal{Subject: "eve", Roles: []string{"developer"}}, "run", Resource{Type: "action"}, false},
		{"action open to the project", Principal{Subject: "eve", Roles: []string{"developer"}}, "run", Resource{Type: "action", Roles: []string{AnyRole}}, true},
		{"action open to the project, off it", Principal{Subject: "eve"}, "run", Resource{Type: "action", Roles: []string{AnyRole}}, false},
		{"a project administrator runs any action", Principal{Subject: "pam", ProjectAdmin: true}, "run", Resource{Type: "action", ProjectID: "PROJ-A"}, true},
		{"a project administrator applies others' changes", Principal{Subject: "pam", ProjectAdmin: true}, "apply", change, true},
		{"a project administrator never their own", Principal{Subject: "carol", ProjectAdmin: true}, "apply", change, false},
		{"a project administrator sets no flag", Principal{Subject: "pam", ProjectAdmin: true}, "flag", Resource{Type: "assignment", ProjectID: "PROJ-A"}, false},
		{"a project administrator writes assignments", Principal{Subject: "pam", ProjectAdmin: true}, "write", Resource{Type: "assignment", ProjectID: "PROJ-A"}, true},
		{"a project administrator does not stop the project", Principal{Subject: "pam", ProjectAdmin: true}, "stop", Resource{Type: "project", ProjectID: "PROJ-A"}, false},
		{"the owner sets the flags", Principal{Subject: "olga", ProjectOwner: true}, "flag", Resource{Type: "assignment", ProjectID: "PROJ-A"}, true},
		{"the owner stops the project", Principal{Subject: "olga", ProjectOwner: true}, "stop", Resource{Type: "project", ProjectID: "PROJ-A"}, true},
		{"the owner moves the project", Principal{Subject: "olga", ProjectOwner: true}, "move", Resource{Type: "project", ProjectID: "PROJ-A"}, true},
		{"the platform administrator stops any project", Principal{Subject: "root", Global: []string{"admin"}}, "stop", Resource{Type: "project", ProjectID: "PROJ-A"}, true},
		{"action off the project", Principal{Subject: "eve"}, "run", Resource{Type: "action"}, false},
		{"action of the role", Principal{Subject: "eve", Roles: []string{"developer"}}, "run", Resource{Type: "action", Roles: []string{"tester", "developer"}}, true},
		{"action of another role", Principal{Subject: "eve", Roles: []string{"developer"}}, "run", Resource{Type: "action", Roles: []string{"tester"}}, false},
		{"agent of another role", Principal{Subject: "eve", Roles: []string{"developer"}}, "run", Resource{Type: "agent", Roles: []string{"tester"}}, false},
		{"anonymous", Principal{Roles: []string{"admin"}}, "read", Resource{Type: "methodology"}, false},
		{"a token's admin role", Principal{Subject: "root", Roles: []string{"admin"}}, "read", Resource{Type: "methodology"}, true},
	}
	for _, tc := range cases {
		got, err := c.Authorize(ctx, Request{Subject: tc.sub, Action: tc.act, Resource: tc.res})
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v err %v, want %v", tc.name, got, err, tc.want)
		}
	}
}

func TestDenyOverridesAndAdmin(t *testing.T) {
	ctx := context.Background()
	c, _ := NewCasbin(nil)
	if err := c.AddPolicy(Policy{Rule: `r.sub.Org == "frozen"`, Resource: "change", Action: "apply", Effect: "deny"}); err != nil {
		t.Fatal(err)
	}
	req := Request{Subject: Principal{Subject: "root", Org: "frozen", Roles: []string{"admin"}}, Action: "apply", Resource: Resource{Type: "change"}}
	if ok, _ := c.Authorize(ctx, req); ok {
		t.Fatal("deny rule must win")
	}
	if err := Check(ctx, c, req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Check: %v", err)
	}
	if n, _ := c.Policies(); len(n) != len(DefaultPolicies)+1 {
		t.Fatalf("policies: %d", len(n))
	}
}

func TestNilAuthorizerDenies(t *testing.T) {
	req := Request{Subject: Principal{Subject: "root", Roles: []string{"admin"}}, Action: "apply", Resource: Resource{Type: "change"}}
	if err := Check(context.Background(), nil, req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a missing authorizer must refuse: %v", err)
	}
	if err := Check(context.Background(), AllowAll{}, req); err != nil {
		t.Fatalf("AllowAll: %v", err)
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(Policy{Rule: `hasRole(r.sub, "x") &&`, Resource: "*", Action: "*", Effect: "allow"}); err == nil {
		t.Error("syntax error must be rejected")
	}
	if err := Validate(Policy{Rule: `hasRole(r.sub, "x")`, Resource: "change", Action: "apply", Effect: "maybe"}); err == nil {
		t.Error("bad effect must be rejected")
	}
	if err := Validate(DefaultPolicies[4]); err != nil {
		t.Errorf("default policy rejected: %v", err)
	}
}

func TestParsePermission(t *testing.T) {
	if r, a, err := ParsePermission("change:apply"); err != nil || r != "change" || a != "apply" {
		t.Fatal(r, a, err)
	}
	if _, _, err := ParsePermission("apply"); err == nil {
		t.Fatal("expected error")
	}
}

func TestStepRolesHeldInAUnitHoldBelowIt(t *testing.T) {
	c, err := NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	step := Resource{Type: "step", Name: "delivery/build", Org: "TEAM-PAY", OrgChain: []string{"TEAM-PAY", "DEP-IT", "ORG-DEFAULT"},
		Owner: "alice", Role: "developer", Accountable: "tech_lead"}
	for _, tc := range []struct {
		who     Principal
		act     string
		allowed bool
	}{
		{Principal{Subject: "bob", Roles: []string{"developer@DEP-IT"}}, "perform", true},      // held above the unit
		{Principal{Subject: "bob", Roles: []string{"developer@TEAM-OPS"}}, "perform", false},   // held elsewhere
		{Principal{Subject: "bob", Roles: []string{"developer"}}, "perform", true},             // unscoped
		{Principal{Subject: "bob", Roles: []string{"tech_lead@TEAM-PAY"}}, "perform", false},   // another role
		{Principal{Subject: "bob", Roles: []string{"tech_lead@TEAM-PAY"}}, "approve", true},    // accountable
		{Principal{Subject: "alice", Roles: []string{"tech_lead@TEAM-PAY"}}, "approve", false}, // never on its own change
	} {
		ok, err := c.Authorize(context.Background(), Request{Subject: tc.who, Action: tc.act, Resource: step})
		if err != nil || ok != tc.allowed {
			t.Fatalf("%v %s: %v %v, want %v", tc.who.Roles, tc.act, ok, err, tc.allowed)
		}
	}
}

// The platform administrator may do everything (ADR 0047, 0139), including what a methodology reserves to its roles:
// a step of a responsible role, an agent or an action of declared roles, an operation no role allows, a seat, its own
// change.
func TestPlatformAdministratorPassesTheRolesOfMethodologies(t *testing.T) {
	ctx := context.Background()
	c, err := NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	admin := Principal{Subject: "root", Org: "acme", Global: []string{"admin"}}
	step := Resource{Type: "step", ID: "p1", Name: "p/review", Org: "acme", ProjectID: "PROJ-A", Owner: "root", Role: "tech_lead", Accountable: "product_owner"}
	for _, req := range []Request{
		{Subject: admin, Action: "perform", Resource: step},
		{Subject: admin, Action: "approve", Resource: step},
		{Subject: admin, Action: "run", Resource: Resource{Type: "action", Name: "review", ProjectID: "PROJ-A", Roles: []string{"tech_lead"}}},
		{Subject: admin, Action: "run", Resource: Resource{Type: "agent", Name: "delivery", ProjectID: "PROJ-A", Roles: []string{"developer"}}},
		{Subject: admin, Action: "perform", Resource: Resource{Type: "operation", Name: "impact.review", ProjectID: "PROJ-A"}},
		{Subject: admin, Action: "sit", Resource: Resource{Type: "decision", ProjectID: "PROJ-A", Roles: []string{"architect"}}},
		{Subject: admin, Action: "apply", Resource: Resource{Type: "change", Owner: "root", ProjectID: "PROJ-A"}},
	} {
		if ok, err := c.Authorize(ctx, req); err != nil || !ok {
			t.Errorf("an administrator may %s %s %s: %v", req.Action, req.Resource.Type, req.Resource.Name, err)
		}
	}
	// a member holding none of the roles may not
	member := Principal{Subject: "ann", Org: "acme", Roles: []string{"developer"}}
	if ok, _ := c.Authorize(ctx, Request{Subject: member, Action: "perform", Resource: step}); ok {
		t.Fatal("a developer does not perform a tech lead's step")
	}
}
