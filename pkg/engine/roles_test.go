package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/methodology"
)

// rolesYAML declares who may run its agents and actions with its own roles (ADR 0043): a release only a
// release_manager runs, by an agent any developer or release_manager starts, and an audit agent only an auditor
// starts.
const rolesYAML = `
name: roled
version: 1.0.0
namespace: alm
roles:
  - {name: developer}
  - {name: release_manager}
  - {name: auditor}
conditions:
  - {name: built, expr: 'artifacts.exists(a, a.type == "build")'}
  - {name: released, expr: 'artifacts.exists(a, a.type == "release")'}
actions:
  - {name: build, kind: builtin, builtin: test.emit, effects: {built: true}, params: {kind: artifact, type: build}}
  - {name: release, kind: builtin, builtin: test.emit, roles: [release_manager], pre: {built: true}, effects: {released: true}, params: {kind: artifact, type: release}}
  - {name: audit, kind: builtin, builtin: test.emit, effects: {built: true}, params: {kind: artifact, type: build}}
goals:
  - {name: ship, pre: {released: true}}
  - {name: audited, pre: {built: true}}
agents:
  - {name: shipper, roles: [developer, release_manager], actions: [build, release], goals: [ship]}
  - {name: auditing, roles: [auditor], actions: [audit], goals: [audited]}
`

func rolesEngine(t *testing.T) *Engine {
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
	e.Methodologies.(StaticMethodologies)["roled"] = c
	// test.emit writes an artifact of the type its params name
	builtins := e.Executors[methodology.KindBuiltin].(BuiltinExecutor)
	builtins["test.emit"] = func(_ context.Context, ac ActionContext) (ActionResult, error) {
		typ, _ := ac.Action.Params["type"].(string)
		return ActionResult{Items: []ItemInput{{Kind: "artifact", Type: typ}}}, nil
	}
	return e
}

func TestRolesUndeclaredByTheMethodologyAreRefused(t *testing.T) {
	m, err := methodology.Parse([]byte(`
name: bad
version: 1.0.0
roles: [{name: developer}]
conditions: [{name: done, expr: 'artifacts.exists(a, a.type == "x")'}]
actions: [{name: a, kind: human, roles: [tester], effects: {done: true}}]
goals: [{name: g, pre: {done: true}}]
agents: [{name: x, roles: [auditor], actions: [a]}]
`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Compile()
	var issues methodology.Issues
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
	if _, err := e.Start(dev, StartRequest{Methodology: "roled", Goal: "audited", Intent: "audit", ProjectID: testProject}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a developer started the auditor's agent: %v", err)
	}

	// a developer starts the shipper: building is open to it, releasing is the release manager's
	p, err := e.Start(dev, StartRequest{Methodology: "roled", Goal: "ship", Intent: "ship it", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(dev, p.ID)
	if p.Status != StatusWaiting || p.Pending == nil || p.Pending.Kind != TaskApproval || p.Pending.Action != "release" ||
		p.Pending.Permission != PermissionRunAction || len(p.Pending.Roles) != 1 || p.Pending.Roles[0] != "release_manager" {
		t.Fatalf("the release must wait for a release manager: %s %+v %s", p.Status, p.Pending, p.Error)
	}
	if _, err := e.Approve(dev, p.ID, true, "me"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a developer approved the release: %v", err)
	}
	if _, err := e.Approve(rm, p.ID, true, "go"); err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(rm, p.ID)
	if p.Status != StatusCompleted {
		t.Fatalf("released by the release manager: %s %+v %s", p.Status, p.Pending, p.Error)
	}

	// a release manager runs it all at once
	p, err = e.Start(rm, StartRequest{Methodology: "roled", Goal: "ship", Intent: "ship it", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Run(rm, p.ID); p.Status != StatusCompleted {
		t.Fatalf("a release manager ships alone: %s %+v %s", p.Status, p.Pending, p.Error)
	}
}
