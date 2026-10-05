package graphsvc

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/criticality"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/risk"
)

// Writing a derogation asks derogation:sign and that the signatory is the writer (ADR 0075 §2).
func TestItemAuthorizer(t *testing.T) {
	risk.Register()
	c, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	check := authz.Authorizer(c)
	perm, _ := domain.ItemPermissionOf(risk.KindDerogation)
	item := func(signatory string) domain.ChangeItem {
		return domain.ChangeItem{Kind: risk.KindDerogation, Data: map[string]any{"signatory": signatory}}
	}
	for name, tc := range map[string]struct {
		who       authz.Principal
		signatory string
		denied    bool
	}{
		"admin signs":          {authz.Principal{Subject: "alice", Roles: []string{"admin"}}, "alice", false},
		"role signs":           {authz.Principal{Subject: "bob", Roles: []string{"derogation_signatory"}}, "bob", false},
		"member does not":      {authz.Principal{Subject: "carol", Roles: []string{"developer"}}, "carol", true},
		"no one signs for one": {authz.Principal{Subject: "alice", Roles: []string{"admin"}}, "bob", true},
		"the platform expires": {authz.System("derogation"), "bob", false},
		"anonymous does not":   {authz.Principal{}, "", true},
	} {
		err := ItemAuthorizer(check, nil)(authz.With(context.Background(), tc.who), domain.Change{ID: "c"}, item(tc.signatory), perm)
		if tc.denied != errors.Is(err, authz.ErrForbidden) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := ItemAuthorizer(nil, nil)(context.Background(), domain.Change{}, item("x"), perm); err != nil {
		t.Errorf("no authorizer: %v", err)
	}
}

// A derogation also needs the role its criticality level asks of a signatory (ADR 0075 §3): derogation:sign-role with the
// role of the policy resolved for the change.
func TestItemAuthorizerSignatoryRole(t *testing.T) {
	risk.Register()
	c, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	perm, _ := domain.ItemPermissionOf(risk.KindDerogation)
	resolve := func(_ context.Context, _ domain.Change, l criticality.Level) criticality.Policy {
		return criticality.Policy{SignatoryRole: map[criticality.Level]string{criticality.C3: "ciso"}[l]}
	}
	change := func(l string) domain.Change {
		return domain.Change{ID: "c", Data: map[string]any{domain.DataCriticality: l}}
	}
	signer := authz.Principal{Subject: "bob", Roles: []string{"derogation_signatory"}}
	item := domain.ChangeItem{Kind: risk.KindDerogation, Data: map[string]any{"signatory": "bob"}}
	closing := domain.ChangeItem{Kind: risk.KindDerogation, Data: map[string]any{"signatory": "bob", "status": "closed"}}
	for name, tc := range map[string]struct {
		who    authz.Principal
		change domain.Change
		item   domain.ChangeItem
		denied bool
	}{
		"no role asked at C2":           {signer, change("C2"), item, false},
		"C3 asks ciso, a signer is not": {signer, change("C3"), item, true},
		"C3 ciso signs":                 {authz.Principal{Subject: "bob", Roles: []string{"derogation_signatory", "ciso"}}, change("C3"), item, false},
		"an admin signs at C3":          {authz.Principal{Subject: "bob", Roles: []string{"admin"}}, change("C3"), item, false},
		"closing asks no role":          {signer, change("C3"), closing, false},
	} {
		err := ItemAuthorizer(c, resolve)(authz.With(context.Background(), tc.who), tc.change, tc.item, perm)
		if tc.denied != errors.Is(err, authz.ErrForbidden) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
