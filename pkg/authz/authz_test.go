package authz

import (
	"context"
	"testing"
)

func TestRolePolicy(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		p    Principal
		perm string
		want bool
	}{
		{Principal{Subject: "a", Roles: []string{"admin"}}, PermChangeApply, true},
		{Principal{Subject: "b", Roles: []string{"approver"}}, PermChangeApply, true},
		{Principal{Subject: "c", Roles: []string{"contributor"}}, PermChangeApply, false},
		{Principal{Roles: []string{"admin"}}, PermChangeApply, false}, // anonymous
		{Principal{Subject: "d", Roles: []string{"x"}}, "process:read", false},
	}
	for _, c := range cases {
		if got, _ := DefaultRoles.Allowed(ctx, c.p, c.perm); got != c.want {
			t.Errorf("%+v %s: got %v", c.p, c.perm, got)
		}
	}
	if got, _ := (RolePolicy{"ops": {"change:*"}}).Allowed(ctx, Principal{Subject: "e", Roles: []string{"ops"}}, PermChangeApply); !got {
		t.Error("resource wildcard")
	}
	if From(With(ctx, Principal{Subject: "z"})).Subject != "z" {
		t.Error("context round trip")
	}
}
