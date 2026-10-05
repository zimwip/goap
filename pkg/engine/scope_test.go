package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/methodology"
)

// recorder is an authorizer that remembers the requests it was asked and answers a fixed verdict.
type recorder struct {
	ok   bool
	err  error
	reqs []authz.Request
}

func (r *recorder) Authorize(_ context.Context, req authz.Request) (bool, error) {
	r.reqs = append(r.reqs, req)
	return r.ok, r.err
}

func scopeProcess() *Process {
	return &Process{ID: "p1", Methodology: "m", ChangeID: "c1", Org: "acme", Project: "PROJ-X",
		Initiator: authz.Principal{Subject: "alice", Org: "acme-initiator", Roles: []string{"developer"}}}
}

func scopeEngine(a authz.Authorizer) *Engine {
	return &Engine{Scope: AuthzScope{Authz: a}, Store: NewMemoryStore()}
}

func TestScopePlacementOfAProcess(t *testing.T) {
	ref := scopeEngine(nil).ref(&Process{ID: "p", Initiator: authz.Principal{Subject: "alice", Project: "OTHER", Roles: []string{"dev"}}})
	if ref.Org != "" || ref.Project != "" {
		t.Fatalf("an unplaced process names no organisation and project (the services behind the scope resolve the roots): %+v", ref)
	}
	if a := scopeEngine(nil).ref(scopeProcess()).Actor(); a.Subject != "alice" || a.Project != "PROJ-X" || len(a.Roles) != 1 {
		t.Fatalf("a process acts as its initiator on its project, whatever project their token had: %+v", a)
	}
}

func TestAllowedBuildsTheResourceOfTheProcess(t *testing.T) {
	rec := &recorder{ok: true}
	e, p := scopeEngine(rec), scopeProcess()
	who := authz.Principal{Subject: "bob"}
	if ok, err := e.allowed(context.Background(), p, who, "process:start"); err != nil || !ok {
		t.Fatalf("allowed: %v %v", ok, err)
	}
	got := rec.reqs[0]
	want := authz.Resource{Type: "process", ID: "p1", Org: "acme-initiator", Owner: "alice", Name: "m", ProjectID: "PROJ-X"}
	if got.Subject.Subject != "bob" || got.Action != "start" || fmt.Sprint(got.Resource) != fmt.Sprint(want) {
		t.Fatalf("request: %+v, want resource %+v", got, want)
	}
	// a change permission names the change, not the process
	if _, err := e.allowed(context.Background(), p, who, "change:apply"); err != nil {
		t.Fatal(err)
	}
	if r := rec.reqs[1].Resource; r.Type != "change" || r.ID != "c1" {
		t.Fatalf("change resource: %+v", r)
	}
	// a malformed permission is an error
	if _, err := e.allowed(context.Background(), p, who, "nocolon"); err == nil {
		t.Fatal("a malformed permission must fail")
	}
	// the verdict and the error of the authorizer are passed on
	rec.ok, rec.err = false, errors.New("boom")
	if ok, err := e.allowed(context.Background(), p, who, "process:start"); ok || err == nil {
		t.Fatalf("authorizer error: %v %v", ok, err)
	}
}

func TestAllowedWithCasbin(t *testing.T) {
	e, p := scopeEngine(mustCasbin(t)), scopeProcess()
	ctx := context.Background()
	member := authz.Principal{Subject: "bob", Roles: []string{"developer"}}
	for _, tc := range []struct {
		name string
		who  authz.Principal
		perm string
		want bool
	}{
		{"a member works on the processes of the project", member, "process:start", true},
		{"a holder of no role does not", authz.Principal{Subject: "eve"}, "process:start", false},
		{"an administrator may do anything", authz.Principal{Subject: "root", Roles: []string{"admin"}}, "release:deploy", true},
		{"a member holds no release permission", member, "release:deploy", false},
		{"a release manager deploys", authz.Principal{Subject: "rita", Roles: []string{"release_manager"}}, "release:deploy", true},
		{"never on their own change", authz.Principal{Subject: "alice", Roles: []string{"release_manager"}}, "release:deploy", false},
	} {
		if ok, err := e.allowed(ctx, p, tc.who, tc.perm); err != nil || ok != tc.want {
			t.Errorf("%s: %v %v, want %v", tc.name, ok, err, tc.want)
		}
	}
}

func TestNilAuthzGrantsEverything(t *testing.T) {
	e, p := scopeEngine(nil), scopeProcess()
	ctx, nobody := context.Background(), authz.Principal{}
	if ok, err := e.allowed(ctx, p, nobody, "nocolon"); !ok || err != nil {
		t.Fatalf("allowed: %v %v", ok, err)
	}
	if ok, err := e.mayRun(ctx, p, nobody, "action", "a", []string{"x"}); !ok || err != nil {
		t.Fatalf("mayRun: %v %v", ok, err)
	}
	sc := &StepContext{Path: "s", Roles: &methodology.Responsibilities{Responsible: "r", Accountable: "a"}}
	if ok, err := e.stepAllowed(ctx, p, nobody, sc, "perform"); !ok || err != nil {
		t.Fatalf("stepAllowed: %v %v", ok, err)
	}
	if ok, err := e.mayUnblock(ctx, p, nobody); !ok || err != nil {
		t.Fatalf("mayUnblock: %v %v", ok, err)
	}
}

func TestMayRunByRoles(t *testing.T) {
	e, p := scopeEngine(mustCasbin(t)), scopeProcess()
	ctx := context.Background()
	dev := authz.Principal{Subject: "bob", Roles: []string{"developer"}}
	rm := authz.Principal{Subject: "rita", Roles: []string{"release_manager"}}
	for _, tc := range []struct {
		name  string
		who   authz.Principal
		roles []string
		want  bool
	}{
		{"one of the roles", rm, []string{"developer", "release_manager"}, true},
		{"none of the roles", dev, []string{"release_manager"}, false},
		{"no role required: any member", dev, nil, true},
		{"no role required: not a stranger", authz.Principal{Subject: "eve"}, nil, false},
	} {
		if ok, err := e.mayRun(ctx, p, tc.who, "action", "release", tc.roles); err != nil || ok != tc.want {
			t.Errorf("%s: %v %v, want %v", tc.name, ok, err, tc.want)
		}
	}
	// the request names the process, its project and organisation
	rec := &recorder{ok: true}
	if _, err := scopeEngine(rec).mayRun(ctx, p, dev, "agent", "shipper", []string{"developer"}); err != nil {
		t.Fatal(err)
	}
	got := rec.reqs[0]
	want := authz.Resource{Type: "agent", ID: "p1", Name: "shipper", Org: "acme", ProjectID: "PROJ-X", Owner: "alice", Roles: []string{"developer"}}
	if got.Action != "run" || fmt.Sprint(got.Resource) != fmt.Sprint(want) {
		t.Fatalf("request: %+v, want resource %+v", got, want)
	}
}

func TestRunRolesAreTheActionsThenTheAgents(t *testing.T) {
	m, err := methodology.Parse([]byte(rolesYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e := scopeEngine(nil)
	act := func(name string) methodology.Action {
		a, ok := c.Action(name)
		if !ok {
			t.Fatalf("no action %s", name)
		}
		return a
	}
	if r := e.runRoles(c, &Process{Agent: "shipper"}, act("release")); len(r) != 1 || r[0] != "release_manager" {
		t.Fatalf("an action's own roles win: %v", r)
	}
	if r := e.runRoles(c, &Process{Agent: "shipper"}, act("build")); len(r) != 2 {
		t.Fatalf("else those of the agent: %v", r)
	}
	if r := e.runRoles(c, &Process{Agent: "nobody"}, act("build")); r != nil {
		t.Fatalf("an unknown agent gives none: %v", r)
	}
}

func TestStepAllowed(t *testing.T) {
	ctx, p := context.Background(), scopeProcess()
	sc := &StepContext{Path: "delivery/build", Roles: &methodology.Responsibilities{Responsible: "developer", Accountable: "release_manager"}}
	dev := authz.Principal{Subject: "bob", Roles: []string{"developer"}}
	rm := authz.Principal{Subject: "rita", Roles: []string{"release_manager"}}

	// no step, no roles, or no role for the act: left to the permissions of the process, the authorizer is not asked
	deny := &recorder{}
	e := scopeEngine(deny)
	for name, c := range map[string]*StepContext{"no step": nil, "no responsibilities": {Path: "s"},
		"no accountable": {Path: "s", Roles: &methodology.Responsibilities{Responsible: "developer"}}} {
		act := "perform"
		if name == "no accountable" {
			act = "approve"
		}
		if ok, err := e.stepAllowed(ctx, p, dev, c, act); !ok || err != nil {
			t.Errorf("%s: %v %v", name, ok, err)
		}
	}
	if len(deny.reqs) != 0 {
		t.Fatalf("the authorizer must not be asked: %+v", deny.reqs)
	}

	e = scopeEngine(mustCasbin(t))
	for _, tc := range []struct {
		name string
		who  authz.Principal
		act  string
		want bool
	}{
		{"the responsible role performs", dev, "perform", true},
		{"another role does not", rm, "perform", false},
		{"the accountable role approves", rm, "approve", true},
		{"the responsible role does not approve", dev, "approve", false},
		{"never their own work", authz.Principal{Subject: "alice", Roles: []string{"release_manager"}}, "approve", false},
	} {
		if ok, err := e.stepAllowed(ctx, p, tc.who, sc, tc.act); err != nil || ok != tc.want {
			t.Errorf("%s: %v %v, want %v", tc.name, ok, err, tc.want)
		}
	}

	// the request names the step
	rec := &recorder{ok: true}
	if _, err := scopeEngine(rec).stepAllowed(ctx, p, dev, sc, "approve"); err != nil {
		t.Fatal(err)
	}
	want := authz.Resource{Type: "step", ID: "p1", Name: "delivery/build", Org: "acme", ProjectID: "PROJ-X", Owner: "alice", Role: "developer", Accountable: "release_manager"}
	if got := rec.reqs[0]; got.Action != "approve" || fmt.Sprint(got.Resource) != fmt.Sprint(want) {
		t.Fatalf("request: %+v, want resource %+v", got, want)
	}
}

func TestMayUnblock(t *testing.T) {
	ctx := context.Background()
	e := scopeEngine(&recorder{}) // denies everything: only the initiator rule can grant
	put := func(p *Process) *Process {
		t.Helper()
		if err := e.Store.Put(ctx, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	proc := func(id, parent, initiator string) *Process {
		return &Process{ID: id, ParentID: parent, Initiator: authz.Principal{Subject: initiator}}
	}
	alice, stranger := authz.Principal{Subject: "alice"}, authz.Principal{Subject: "eve"}

	p := proc("p", "", "alice")
	if ok, err := e.mayUnblock(ctx, p, alice); !ok || err != nil {
		t.Fatalf("the initiator answers for the run: %v %v", ok, err)
	}
	if ok, _ := e.mayUnblock(ctx, p, authz.Principal{}); ok {
		t.Fatal("an anonymous caller never matches an initiator")
	}
	if ok, _ := e.mayUnblock(ctx, p, stranger); ok {
		t.Fatal("a stranger does not answer for the run")
	}

	// the initiator of a run above it, within 16 runs: chain builds n runs, each the child of the next, the last
	// initiated by alice
	chain := func(prefix string, n int) *Process {
		for i := 0; i < n; i++ {
			parent, who := fmt.Sprintf("%s%d", prefix, i+1), "other"
			if i == n-1 {
				parent, who = "", "alice"
			}
			put(proc(fmt.Sprintf("%s%d", prefix, i), parent, who))
		}
		return mustGet(t, e, prefix+"0")
	}
	if ok, err := e.mayUnblock(ctx, chain("a", 16), alice); err != nil || !ok {
		t.Fatalf("an owner 16 runs up: %v %v", ok, err)
	}
	if ok, _ := e.mayUnblock(ctx, chain("b", 17), alice); ok {
		t.Fatal("the walk up stops after 16 runs")
	}
	// a parent that cannot be read ends the walk without an error
	if ok, err := e.mayUnblock(ctx, &Process{ID: "x", ParentID: "gone", Initiator: authz.Principal{Subject: "other"}}, alice); ok || err != nil {
		t.Fatalf("a missing parent: %v %v", ok, err)
	}

	// the accountable role of the step the run carries out
	e = scopeEngine(mustCasbin(t))
	step := &StepContext{Path: "s", Roles: &methodology.Responsibilities{Responsible: "developer", Accountable: "release_manager"}}
	q := &Process{ID: "q", Initiator: authz.Principal{Subject: "alice"}, Step: step}
	rm := authz.Principal{Subject: "rita", Roles: []string{"release_manager"}}
	if ok, err := e.mayUnblock(ctx, q, rm); err != nil || !ok {
		t.Fatalf("the accountable role of the step: %v %v", ok, err)
	}
	// else the permission process:unblock, which the members of the project hold by default
	dev := authz.Principal{Subject: "bob", Roles: []string{"developer"}}
	if ok, err := e.mayUnblock(ctx, q, dev); err != nil || !ok {
		t.Fatalf("a member of the project: %v %v", ok, err)
	}
	if ok, err := e.mayUnblock(ctx, q, stranger); err != nil || ok {
		t.Fatalf("a stranger: %v %v", ok, err)
	}
}

func mustGet(t *testing.T, e *Engine, id string) *Process {
	t.Helper()
	p, err := e.Store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
