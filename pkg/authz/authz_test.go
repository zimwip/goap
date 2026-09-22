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
	change := Resource{Type: "change", ID: "c1", Org: "acme", Owner: "carol"}
	cases := []struct {
		name string
		sub  Principal
		act  string
		res  Resource
		want bool
	}{
		{"admin", Principal{Subject: "root", Roles: []string{"admin"}}, "apply", change, true},
		{"approver same org", Principal{Subject: "alice", Org: "acme", Roles: []string{"approver"}}, "apply", change, true},
		{"four eyes", Principal{Subject: "carol", Org: "acme", Roles: []string{"approver"}}, "apply", change, false},
		{"other org", Principal{Subject: "bob", Org: "globex", Roles: []string{"approver"}}, "apply", change, false},
		{"contributor", Principal{Subject: "dave", Org: "acme", Roles: []string{"contributor"}}, "apply", change, false},
		{"contributor process", Principal{Subject: "dave", Org: "acme", Roles: []string{"contributor"}}, "start", Resource{Type: "process", Org: "acme"}, true},
		{"contributor other org", Principal{Subject: "dave", Org: "acme", Roles: []string{"contributor"}}, "read", Resource{Type: "process", Org: "globex"}, false},
		{"read", Principal{Subject: "eve", Org: "acme"}, "read", Resource{Type: "methodology"}, true},
		{"read same org", Principal{Subject: "eve", Org: "acme"}, "read", Resource{Type: "process", Org: "acme"}, true},
		{"read other org", Principal{Subject: "eve", Org: "acme"}, "read", Resource{Type: "process", Org: "globex"}, false},
		{"write methodology", Principal{Subject: "eve", Org: "acme", Roles: []string{"contributor"}}, "write", Resource{Type: "methodology"}, false},
		{"methodologist", Principal{Subject: "mia", Org: "acme", Roles: []string{"methodologist"}}, "publish", Resource{Type: "methodology", Org: "acme"}, true},
		{"anonymous", Principal{Roles: []string{"admin"}}, "read", Resource{Type: "methodology"}, false},
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
