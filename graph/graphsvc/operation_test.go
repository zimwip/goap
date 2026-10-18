package graphsvc

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/operations"
)

// The roles of a methodology say who performs each operation on its changes (ADR 0103): a reviewer accepts or rejects
// impacts and never withdraws or edits one; a methodology listing no operation restricts nothing.
func TestOperationAuthorizer(t *testing.T) {
	c, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	// the roles of each methodology and the operations they list (none: every operation), as the registry resolves
	// them (methodology.Methodology.OperationRoles)
	type role struct {
		name string
		ops  []string
	}
	methods := map[string][]role{
		"restricted": {{"author", []string{"doer"}}, {"reviewer", []string{"reviewer"}}, {"lead", nil}},
		"open":       {{"author", nil}},
	}
	resolve := func(_ context.Context, name, op string) ([]string, bool, error) {
		rs, ok := methods[name]
		if !ok || !slices.ContainsFunc(rs, func(r role) bool { return len(r.ops) > 0 }) {
			return nil, false, nil
		}
		var roles []string
		for _, r := range rs {
			if len(r.ops) == 0 || operations.Allows(r.ops, op) {
				roles = append(roles, r.name)
			}
		}
		return roles, true, nil
	}
	who := func(roles ...string) authz.Principal { return authz.Principal{Subject: "u", Roles: roles} }
	for name, tc := range map[string]struct {
		who         authz.Principal
		methodology string
		op          string
		denied      bool
	}{
		"reviewer reviews":           {who("reviewer"), "restricted", operations.ImpactReview, false},
		"reviewer does not edit":     {who("reviewer"), "restricted", operations.ImpactUpdate, true},
		"reviewer does not withdraw": {who("reviewer"), "restricted", operations.ImpactWithdraw, true},
		"author edits":               {who("author"), "restricted", operations.ImpactUpdate, false},
		"author does not review":     {who("author"), "restricted", operations.ImpactReview, true},
		"lead does both":             {who("lead"), "restricted", operations.ImpactReview, false},
		"outsider does nothing":      {who("guest"), "restricted", operations.ChangeCommit, true},
		"admin does everything":      {who("admin"), "restricted", operations.ImpactWithdraw, false},
		"open methodology":           {who("guest"), "open", operations.ImpactWithdraw, false},
		"unknown methodology":        {who("guest"), "nope", operations.ImpactWithdraw, false},
		"the platform acts":          {authz.System("seed"), "restricted", operations.ImpactUpdate, false},
	} {
		err := OperationAuthorizer(c, resolve)(authz.With(context.Background(), tc.who), domain.Change{ID: "c", Methodology: tc.methodology}, tc.op)
		if tc.denied != errors.Is(err, authz.ErrForbidden) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := OperationAuthorizer(nil, resolve)(authz.With(context.Background(), who("guest")), domain.Change{Methodology: "restricted"}, operations.ImpactUpdate); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("no authorizer must refuse (ADR 0138): %v", err)
	}
}
