package enginee2e

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/engine/engine"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
)

func TestChangeAsksAddressTheRoleAndTellWhoMayAnswer(t *testing.T) {
	e := rolesEngine(t)
	dev := authz.With(context.Background(), authz.Principal{Subject: "dev", Roles: []string{"developer"}})
	rm := authz.With(context.Background(), authz.Principal{Subject: "rm", Roles: []string{"release_manager"}})

	p, err := e.Start(dev, engine.StartRequest{Methodology: "roled", Goal: "ship", Intent: "ship it", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(dev, p.ID)
	if p.ChangeID == "" || p.Pending == nil || p.Pending.Kind != engine.TaskApproval {
		t.Fatalf("the release must wait for a release manager on a change: %s %+v", p.ChangeID, p.Pending)
	}

	asks, err := e.ChangeAsks(dev, p.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(asks) != 1 || asks[0].Kind != engine.TaskApproval || asks[0].ProcessID != p.ID || asks[0].Action != "release" ||
		len(asks[0].Roles) != 1 || asks[0].Roles[0] != "release_manager" || asks[0].Owner != "" {
		t.Fatalf("one approval addressed to the release manager: %+v", asks)
	}
	if asks[0].MayAnswer {
		t.Fatal("a developer may not answer the release manager's approval")
	}
	if asks, err = e.ChangeAsks(rm, p.ChangeID); err != nil || len(asks) != 1 || !asks[0].MayAnswer {
		t.Fatalf("the release manager answers it: %+v %v", asks, err)
	}

	// across changes (ADR 0158): the same ask, under its change; a change gone is left out
	byChange, err := e.Asks(rm, []domain.ChangeID{p.ChangeID, "CH-gone"})
	if err != nil || len(byChange) != 1 || len(byChange[p.ChangeID]) != 1 || !byChange[p.ChangeID][0].MayAnswer {
		t.Fatalf("the asks of the change, by change: %+v %v", byChange, err)
	}

	if _, err := e.Approve(rm, p.ID, true, "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(rm, p.ID); err != nil {
		t.Fatal(err)
	}
	if asks, err = e.ChangeAsks(dev, p.ChangeID); err != nil || len(asks) != 0 {
		t.Fatalf("nothing is asked once approved: %+v %v", asks, err)
	}
	if byChange, err = e.Asks(dev, []domain.ChangeID{p.ChangeID}); err != nil || len(byChange) != 0 {
		t.Fatalf("a change waiting on nothing is left out: %+v %v", byChange, err)
	}
}

// directory gives the subjects of a test their roles on the project and its administrators, as the access snapshot
// does (a principal named by its subject alone, as an assignee is judged, holds what the directory says), then asks
// the casbin rules.
type directory struct {
	authz.Authorizer
	roles  map[string][]string
	admins map[string]bool
}

func (d directory) Authorize(ctx context.Context, req authz.Request) (bool, error) {
	if r, ok := d.roles[req.Subject.Subject]; ok && len(req.Subject.Roles) == 0 {
		req.Subject.Roles = r
	}
	req.Subject.ProjectAdmin = d.admins[req.Subject.Subject] && req.Resource.ProjectID != ""
	return d.Authorizer.Authorize(ctx, req)
}

func TestHumanTasksAreAcceptedRejectedAndAssigned(t *testing.T) {
	e := rolesEngine(t)
	e.Scope = engine.AuthzScope{Authz: directory{Authorizer: mustCasbin(t),
		roles:  map[string][]string{"dev": {"developer"}, "rm": {"release_manager"}, "rm2": {"release_manager"}},
		admins: map[string]bool{"padmin": true}}}
	as := func(s string) context.Context { return authz.With(context.Background(), authz.Principal{Subject: s}) }
	ask := func(who string, change domain.ChangeID) engine.Ask {
		t.Helper()
		asks, err := e.ChangeAsks(as(who), change)
		if err != nil || len(asks) != 1 {
			t.Fatalf("one ask for %s: %+v %v", who, asks, err)
		}
		return asks[0]
	}

	p, err := e.Start(as("dev"), engine.StartRequest{Methodology: "roled", Goal: "ship", Intent: "ship it", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(as("dev"), p.ID)
	if p.Pending == nil || p.Pending.Kind != engine.TaskApproval {
		t.Fatalf("the release waits for a release manager: %+v", p.Pending)
	}
	if a := ask("rm", p.ChangeID); !a.MayAccept || !a.MayAnswer || a.Assignee != "" || a.MayAssign {
		t.Fatalf("a release manager may take the approval, not assign it: %+v", a)
	}
	if a := ask("dev", p.ChangeID); a.MayAccept {
		t.Fatalf("a developer may not take it: %+v", a)
	}
	if _, err := e.AcceptTask(as("dev"), p.ID); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a developer may not accept it: %v", err)
	}

	// accepted: the holder's alone
	if _, err := e.AcceptTask(as("rm"), p.ID); err != nil {
		t.Fatal(err)
	}
	if a := ask("rm2", p.ChangeID); a.Assignee != "rm" || a.AssignedBy != "rm" || a.MayAnswer || a.MayAccept {
		t.Fatalf("held by rm, the other release manager no longer answers it: %+v", a)
	}
	if _, err := e.AcceptTask(as("rm2"), p.ID); !errors.Is(err, engine.ErrInvalidState) {
		t.Fatalf("a task held is not taken: %v", err)
	}
	if _, err := e.Approve(as("rm2"), p.ID, true, "go"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("only its holder approves it: %v", err)
	}

	// assigned by an administrator of the project, to someone who may answer it
	if _, err := e.AssignTask(as("rm"), p.ID, "rm2"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a member does not assign tasks: %v", err)
	}
	if a := ask("padmin", p.ChangeID); !a.MayAssign {
		t.Fatalf("the administrator of the project assigns it: %+v", a)
	}
	if _, err := e.AssignTask(as("padmin"), p.ID, "dev"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a developer may not be given the release manager's approval: %v", err)
	}
	if got, err := e.TaskCandidates(as("padmin"), p.ID, []string{"dev", "rm", "rm2", "rm"}); err != nil || !slices.Equal(got, []string{"rm", "rm2"}) {
		t.Fatalf("the release managers may be given it: %v %v", got, err)
	}
	if _, err := e.AssignTask(as("padmin"), p.ID, "rm2"); err != nil {
		t.Fatal(err)
	}
	if a := ask("rm", p.ChangeID); a.Assignee != "rm2" || a.AssignedBy != "padmin" || a.MayAnswer {
		t.Fatalf("given to rm2: %+v", a)
	}

	// given back: offered again, no longer to who turned it down
	if _, err := e.RejectTask(as("rm2"), p.ID, "on leave"); err != nil {
		t.Fatal(err)
	}
	if a := ask("rm2", p.ChangeID); a.Assignee != "" || !a.Declined {
		t.Fatalf("given back and declined: %+v", a)
	}
	if a := ask("rm", p.ChangeID); a.Declined || !a.MayAccept || !a.MayAnswer {
		t.Fatalf("offered again to the others: %+v", a)
	}
	if _, err := e.Approve(as("rm"), p.ID, true, "go"); err != nil {
		t.Fatal(err)
	}
}
