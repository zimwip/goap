package registrysvc

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/def"
)

func almDomain(version string) def.Domain {
	return def.Domain{Name: "alm", Version: version, Schema: def.Schema{
		NodeTypes: []def.NodeType{{Name: "Requirement"}, {Name: "TestCase", Attributes: []def.Attribute{{Name: "title"}}}},
		LinkTypes: []def.LinkType{{Name: "verifies", From: "TestCase", To: "Requirement"}},
	}}
}

// refMeth acts on alm and relies on its link type alm@verifies.
func refMeth(name string) methodology.Methodology {
	return methodology.Methodology{
		Name: name, Version: "1", Namespace: "alm",
		Conditions: []methodology.Condition{{Name: "c", Expr: `changeImpacts.exists(n, n.hasPost && n.post.out.exists(l, l.type == "alm@verifies"))`}},
		Actions:    []methodology.Action{{Name: "a", Kind: methodology.KindHuman, Effects: map[string]bool{"c": true}}},
		Goals:      []methodology.Goal{{Name: "g", Pre: map[string]bool{"c": true}}},
	}
}

// subjects records the subjects published.
type subjects struct {
	mu   sync.Mutex
	list []string
}

func (r *subjects) Publish(_ context.Context, subject string, v any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev, _ := v.(map[string]string)
	r.list = append(r.list, strings.TrimPrefix(subject, "goap.registry.methodology.")+":"+ev["name"]+"@"+ev["version"])
	return nil
}

func (r *subjects) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.list
	r.list = nil
	return out
}

// A methodology is validated and compiled against the published domains of the domain registry (ADR 0125 §3), pins
// the versions it was published against (ADR 0113), and lists as a user of the domain.
func TestMethodologiesFollowTheDomainRegistry(t *testing.T) {
	enf, _ := authz.NewCasbin(nil)
	s := &Service{Store: NewGraphStore(graph.New(graph.NewMemory())), Authz: enf}
	ctx := as("admin")
	if _, _, err := domainsOf(s).SaveDomain(ctx, almDomain("1")); err != nil {
		t.Fatal(err)
	}
	// a draft domain does not count
	if _, issues, err := s.Save(ctx, refMeth("uses")); err != nil || len(issues) == 0 {
		t.Fatalf("no published alm domain yet: %v %v", issues, err)
	}
	if _, err := s.Publish(ctx, "uses", "1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("publish on a draft domain: %v", err)
	}
	if _, err := domainsOf(s).PublishDomain(ctx, "alm", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(ctx, "uses", "1"); err != nil {
		t.Fatalf("publish methodology: %v", err)
	}
	if c, err := s.Methodology(ctx, "uses"); err != nil || c.Types == nil || !c.Types.HasLinkType("alm@verifies") {
		t.Fatalf("the compiled methodology sees the types in force: %v", err)
	}
	if rec, err := s.Get(ctx, "uses", "1"); err != nil || rec.Methodology.Domains["alm"] != "1" {
		t.Fatalf("pinned at publication: %+v %v", rec.Methodology.Domains, err)
	}
	users, err := s.DomainUsage(ctx, "alm", "1")
	if err != nil || len(users) != 1 || users[0].Methodology.Name != "uses" {
		t.Fatalf("usage: %v %v", users, err)
	}

	// a compatible version reaches no published methodology: it keeps compiling against its pin
	v2 := almDomain("2")
	v2.NodeTypes = append(v2.NodeTypes, def.NodeType{Name: "Need"})
	if _, _, err := domainsOf(s).ImportDomain(ctx, mustYAML(t, v2), true); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Methodology(ctx, "uses"); err != nil || c.Types.HasNodeType("alm@Need") {
		t.Fatalf("the methodology compiles against its pinned version: %v", err)
	}
	// a new version of the methodology moves to the latest domains
	if _, err := s.CreateVersion(ctx, "uses", "1", "2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(ctx, "uses", "2"); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Methodology(ctx, "uses"); err != nil || c.Version != "2" || !c.Types.HasNodeType("alm@Need") {
		t.Fatalf("version 2 is written against alm@2: %v", err)
	}
	// the version a methodology pins serves it once archived
	if err := domainsOf(s).DeleteDomain(ctx, "alm", "1"); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Methodology(ctx, "uses@1"); err != nil || !c.Types.HasLinkType("alm@verifies") || c.Types.HasNodeType("alm@Need") {
		t.Fatalf("an archived pinned version still serves its methodology: %v", err)
	}
}

// A domain version that breaks a published methodology is published all the same; the methodology registry checks its
// methodologies again on the event and suspends the broken ones, never the domain (ADR 0125 §3): no change starts on a
// suspended version, no run reads it, the lists leave it out. A compatible domain version, or a methodology version
// written against the new domain, brings it back.
func TestADomainThatBreaksAMethodologySuspendsIt(t *testing.T) {
	enf, _ := authz.NewCasbin(nil)
	ev := &subjects{}
	s := &Service{Store: NewMemoryStore(), Authz: enf, Events: ev, now: tickingClock()}
	ctx := as("admin")
	d := domainsOf(s)
	if _, _, err := d.ImportDomain(ctx, mustYAML(t, almDomain("1")), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.importPublished(ctx, refMeth("uses")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Recheck(ctx); err != nil || len(got) != 0 {
		t.Fatalf("in force: %v %v", got, err)
	}
	ev.take()

	// alm@2 drops the link type uses@1 relies on
	v2 := almDomain("2")
	v2.LinkTypes = nil
	if _, _, err := d.ImportDomain(ctx, mustYAML(t, v2), true); err != nil {
		t.Fatalf("a domain is never refused for a methodology: %v", err)
	}
	if got := ev.take(); !slices.Equal(got, []string{"suspended:uses@1"}) {
		t.Fatalf("the suspension is published: %v", got)
	}
	r, _ := s.Get(ctx, "uses", "1")
	if why := s.Suspended(ctx, r); !strings.Contains(why, "alm@verifies") {
		t.Fatalf("the reason names what broke: %q", why)
	}
	if _, err := s.Methodology(ctx, "uses"); !errors.Is(err, ErrSuspended) {
		t.Fatalf("no run reads it: %v", err)
	}
	if _, err := s.MethodologyVersion(ctx, "uses"); !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("no change starts on it: %v", err)
	}
	if cs, err := s.List(ctx); err != nil || len(cs) != 0 {
		t.Fatalf("the list leaves it out: %v %v", cs, err)
	}

	// a version written against alm@2 is published (checked with the domains in force), the suspended one stays so
	m2 := refMeth("uses")
	m2.Version = "2"
	m2.Conditions = []methodology.Condition{{Name: "c", Expr: `changeImpacts.exists(n, n.hasPost && n.post.type == "alm@TestCase")`}}
	if _, _, err := s.importPublished(ctx, m2); err != nil {
		t.Fatal(err)
	}
	if v, err := s.MethodologyVersion(ctx, "uses"); err != nil || v != "2" {
		t.Fatalf("new changes take the version in force: %q %v", v, err)
	}
	if cs, err := s.List(ctx); err != nil || len(cs) != 1 || cs[0].Version != "2" {
		t.Fatalf("listed again: %v", err)
	}

	// alm@3 brings the link type back: uses@1 comes back in force
	ev.take()
	if _, _, err := d.ImportDomain(ctx, mustYAML(t, almDomain("3")), true); err != nil {
		t.Fatal(err)
	}
	if got := ev.take(); !slices.Equal(got, []string{"resumed:uses@1"}) {
		t.Fatalf("the return is published: %v", got)
	}
	if _, err := s.Methodology(ctx, "uses@1"); err != nil {
		t.Fatalf("back in force: %v", err)
	}
}
