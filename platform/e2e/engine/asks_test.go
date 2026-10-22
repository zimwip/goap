package enginee2e

import (
	"context"
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
