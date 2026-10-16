package enginee2e

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/engine/engine"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/lib/def"
)

// rolesYAML (pkg/engine/testdata/roles.yaml) declares who may run its agents and actions with its own roles (ADR 0043): a release only a
// release_manager runs, by an agent any developer or release_manager starts, and an audit agent only an auditor
// starts.
var rolesYAML = readRoles()

func rolesEngine(t *testing.T) *engine.Engine {
	t.Helper()
	e, _, _ := setup(t)
	m, err := methodology.Parse([]byte(rolesYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(engine.StaticMethodologies)["roled"] = c
	// test.emit writes an artifact of the type its params name
	builtins := e.Executors[methodology.KindBuiltin].(engine.BuiltinExecutor)
	builtins["test.emit"] = func(_ context.Context, ac engine.ActionContext) (engine.ActionResult, error) {
		typ, _ := ac.Action.Params["target"].(string)
		return engine.ActionResult{Items: []engine.ItemInput{{Type: "artifact", Target: typ}}}, nil
	}
	return e
}

func TestRolesUndeclaredByTheMethodologyAreRefused(t *testing.T) {
	m, err := methodology.Parse([]byte(`
name: bad
version: 1.0.0
roles: [{name: developer}]
conditions: [{name: done, expr: 'artifacts.exists(a, a.target == "x")'}]
actions: [{name: a, kind: human, roles: [tester], effects: {done: true}}]
goals: [{name: g, pre: {done: true}}]
agents: [{name: x, roles: [auditor], actions: [a]}]
`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Compile()
	var issues def.Issues
	if !errors.As(err, &issues) {
		t.Fatalf("expected issues, got %v", err)
	}
	var paths []string
	for _, i := range issues {
		paths = append(paths, i.Path)
	}
	if len(paths) != 2 || paths[0] != "actions[0].roles[0]" || paths[1] != "agents[0].roles[0]" {
		t.Fatalf("undeclared roles must be reported: %v", issues)
	}
}

func TestAgentsAndActionsRunByTheirRoles(t *testing.T) {
	e := rolesEngine(t)
	dev := authz.With(context.Background(), authz.Principal{Subject: "dev", Roles: []string{"developer"}})
	rm := authz.With(context.Background(), authz.Principal{Subject: "rm", Roles: []string{"release_manager"}})

	// an agent its initiator holds no role of may not be started
	if _, err := e.Start(dev, engine.StartRequest{Methodology: "roled", Goal: "audited", Intent: "audit", ProjectID: testProject}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a developer started the auditor's agent: %v", err)
	}

	// a developer starts the shipper: building is open to it, releasing is the release manager's
	p, err := e.Start(dev, engine.StartRequest{Methodology: "roled", Goal: "ship", Intent: "ship it", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(dev, p.ID)
	if p.Status != engine.StatusWaiting || p.Pending == nil || p.Pending.Kind != engine.TaskApproval || p.Pending.Action != "release" ||
		p.Pending.Permission != engine.PermissionRunAction || len(p.Pending.Roles) != 1 || p.Pending.Roles[0] != "release_manager" {
		t.Fatalf("the release must wait for a release manager: %s %+v %s", p.Status, p.Pending, p.Error)
	}
	if _, err := e.Approve(dev, p.ID, true, "me"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a developer approved the release: %v", err)
	}
	if _, err := e.Approve(rm, p.ID, true, "go"); err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(rm, p.ID)
	if p.Status != engine.StatusCompleted {
		t.Fatalf("released by the release manager: %s %+v %s", p.Status, p.Pending, p.Error)
	}

	// a release manager runs it all at once
	p, err = e.Start(rm, engine.StartRequest{Methodology: "roled", Goal: "ship", Intent: "ship it", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Run(rm, p.ID); p.Status != engine.StatusCompleted {
		t.Fatalf("a release manager ships alone: %s %+v %s", p.Status, p.Pending, p.Error)
	}
}
