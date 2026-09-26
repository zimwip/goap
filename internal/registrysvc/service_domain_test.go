package registrysvc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/methodology"
)

func almDomain(version string) methodology.Domain {
	return methodology.Domain{Name: "alm", Version: version, Schema: methodology.Schema{
		NodeTypes: []methodology.NodeType{{Name: "Requirement"}, {Name: "TestCase", Properties: []string{"title"}}},
		LinkTypes: []methodology.LinkType{{Name: "verifies", From: "TestCase", To: "Requirement"}},
	}}
}

func refMeth(ref string) methodology.Methodology {
	return methodology.Methodology{
		Name: "uses", Version: "1", DomainRef: ref,
		Conditions: []methodology.Condition{{Name: "c", Expr: `proposals.exists(l, l.link.type == "verifies")`}},
		Actions:    []methodology.Action{{Name: "a", Kind: methodology.KindHuman, Effects: map[string]bool{"c": true}}},
		Goals:      []methodology.Goal{{Name: "g", Pre: map[string]bool{"c": true}}},
	}
}

func TestDomainLifecycle(t *testing.T) {
	for name, mk := range stores(t) {
		t.Run(name, func(t *testing.T) {
			enf, _ := authz.NewCasbin(nil)
			s := &Service{Store: mk(t), Authz: enf}
			ctx := as("methodologist")

			if _, _, err := s.SaveDomain(as("contributor"), almDomain("1")); !errors.Is(err, authz.ErrForbidden) {
				t.Fatalf("contributor save: %v", err)
			}
			rec, issues, err := s.SaveDomain(ctx, almDomain("1"))
			if err != nil || len(issues) > 0 || rec.Status != StatusDraft {
				t.Fatalf("save: %+v %v %v", rec, issues, err)
			}
			got, err := s.GetDomain(ctx, "alm", "1")
			if err != nil || len(got.Domain.NodeTypes) != 2 || got.Domain.NodeTypes[1].Properties[0] != "title" || got.Domain.LinkTypes[0].To != "Requirement" {
				t.Fatalf("get: %+v %v", got, err)
			}
			if _, err := s.GetDomain(ctx, "alm", ""); !errors.Is(err, ErrNotFound) {
				t.Fatalf("no published version yet: %v", err)
			}

			// a methodology cannot be published on a draft domain
			if _, _, err := s.Save(ctx, refMeth("alm@1")); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Publish(ctx, "uses", "1"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "publish it first") {
				t.Fatalf("publish on draft domain: %v", err)
			}
			if _, err := s.PublishDomain(ctx, "alm", "1"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Publish(ctx, "uses", "1"); err != nil {
				t.Fatalf("publish methodology: %v", err)
			}
			if c, err := s.Methodology(ctx, "uses"); err != nil || len(c.Domain.NodeTypes) != 2 {
				t.Fatalf("compiled methodology must see the shared domain: %v", err)
			}
			if r, err := s.GetResolved(ctx, "uses", "1"); err != nil || len(r.Methodology.Domain.LinkTypes) != 1 {
				t.Fatalf("resolved get: %v", err)
			}
			if st, _ := s.Get(ctx, "uses", "1"); len(st.Methodology.Domain.NodeTypes) != 0 || st.Methodology.DomainRef != "alm@1" {
				t.Fatalf("stored definition must keep the reference only: %+v", st.Methodology)
			}

			// published versions are immutable; usage blocks archiving
			if _, _, err := s.SaveDomain(ctx, almDomain("1")); !errors.Is(err, ErrImmutable) {
				t.Fatalf("save published: %v", err)
			}
			users, err := s.DomainUsage(ctx, "alm", "1")
			if err != nil || len(users) != 1 || users[0].Methodology.Name != "uses" {
				t.Fatalf("usage: %v %v", users, err)
			}
			if err := s.DeleteDomain(ctx, "alm", "1"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("archive used domain: %v", err)
			}

			// a new version drops a type still used: the pinned methodology is unaffected
			v2, err := s.CreateDomainVersion(ctx, "alm", "1", "2")
			if err != nil || v2.Status != StatusDraft {
				t.Fatalf("new version: %+v %v", v2, err)
			}
			d := v2.Domain
			d.LinkTypes = nil
			if _, _, err := s.SaveDomain(ctx, d); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PublishDomain(ctx, "alm", "2"); err != nil {
				t.Fatalf("pinned methodologies do not block a new version: %v", err)
			}
			if _, err := s.Methodology(ctx, "uses"); err != nil {
				t.Fatalf("pinned methodology still compiles: %v", err)
			}

			// a floating methodology follows the latest published domain and blocks a breaking one
			fl := refMeth("alm")
			fl.Name = "floating"
			if _, _, err := s.Save(ctx, fl); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Publish(ctx, "floating", "1"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("floating on v2 (no verifies link type): %v", err)
			}
			if _, _, err := s.Save(ctx, refMeth("alm@1")); !errors.Is(err, ErrImmutable) {
				t.Fatalf("published methodology is immutable: %v", err)
			}
			v3 := almDomain("3")
			if _, _, err := s.SaveDomain(ctx, v3); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PublishDomain(ctx, "alm", "3"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Publish(ctx, "floating", "1"); err != nil {
				t.Fatalf("floating on v3: %v", err)
			}
			if err := s.DeleteDomain(ctx, "alm", "3"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("archiving the version a floating methodology resolves to: %v", err)
			}
			v4 := almDomain("4")
			v4.LinkTypes = nil
			if _, _, err := s.SaveDomain(ctx, v4); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PublishDomain(ctx, "alm", "4"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "would break") {
				t.Fatalf("breaking domain version: %v", err)
			}

			// drafts are deletable, lists show the latest published per name
			if err := s.DeleteDomain(ctx, "alm", "4"); err != nil {
				t.Fatal(err)
			}
			list, err := s.DomainVersions(ctx, false)
			if err != nil || len(list) != 1 || list[0].Domain.Version != "3" {
				t.Fatalf("latest: %+v %v", list, err)
			}
			all, _ := s.DomainVersions(ctx, true)
			if len(all) != 3 {
				t.Fatalf("all versions: %d", len(all))
			}
		})
	}
}

func TestMethodologyRejectsEmbeddedAndReferencedDomain(t *testing.T) {
	enf, _ := authz.NewCasbin(nil)
	s := &Service{Store: NewMemoryStore(), Authz: enf}
	ctx := as("methodologist")
	if _, _, err := s.SaveDomain(ctx, almDomain("1")); err != nil {
		t.Fatal(err)
	}
	m := refMeth("alm@1")
	m.Domain = methodology.Schema{NodeTypes: []methodology.NodeType{{Name: "X"}}}
	_, issues, err := s.Save(ctx, m)
	if err != nil || len(issues) != 1 || issues[0].Path != "domainRef" {
		t.Fatalf("issues: %v %v", issues, err)
	}
}

func TestMissingDomainMessage(t *testing.T) {
	enf, _ := authz.NewCasbin(nil)
	s := &Service{Store: NewMemoryStore(), Authz: enf}
	_, issues, err := s.Save(as("methodologist"), refMeth("nope@9"))
	if err != nil || len(issues) != 1 || issues[0].Message != "nope@9: domain not found" {
		t.Fatalf("issues: %v %v", issues, err)
	}
}

func TestDomainImportExport(t *testing.T) {
	enf, _ := authz.NewCasbin(nil)
	s := &Service{Store: NewMemoryStore(), Authz: enf}
	ctx := as("methodologist")
	src := "name: alm\nversion: \"1\"\nnodeTypes:\n  - name: Need\n  - name: Requirement\n    extends: Need\nlinkTypes:\n  - name: derives\n    from: Requirement\n    to: Need\n"
	r, _, err := s.ImportDomain(ctx, []byte(src), true)
	if err != nil || r.Status != StatusPublished {
		t.Fatalf("import: %+v %v", r, err)
	}
	out, name, err := s.ExportDomain(ctx, "alm", "1")
	if err != nil || name != "domain-alm-1.yaml" || !strings.Contains(string(out), "extends: Need") {
		t.Fatalf("export: %s %s %v", name, out, err)
	}
}

func TestSeedDomains(t *testing.T) {
	dir := t.TempDir()
	src := "name: alm\nversion: \"1\"\nnodeTypes:\n  - name: Need\n"
	if err := os.WriteFile(filepath.Join(dir, "alm.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: NewMemoryStore()}
	got, err := s.SeedDomains(as("admin"), dir)
	if err != nil || len(got) != 1 || got[0] != "alm@1" {
		t.Fatalf("seed: %v %v", got, err)
	}
	if got, err := s.SeedDomains(as("admin"), dir); err != nil || len(got) != 0 {
		t.Fatalf("seed again must skip stored versions: %v %v", got, err)
	}
	if r, err := s.GetDomain(as("admin"), "alm", ""); err != nil || r.Status != StatusPublished {
		t.Fatalf("published: %+v %v", r, err)
	}
}

func TestDomainComposedOfNodeTypesLinkTypesAndLifecycles(t *testing.T) {
	yaml := `
name: docs
version: 1.0.0
lifecycles:
  - name: req
    initial: draft
    states: [{name: draft, editable: true}, {name: approved}]
    transitions: [{name: approve, from: draft, to: approved, permission: "requirement:approve", requires: {attributes: [title]}}]
nodeTypes:
  - {name: Requirement, lifecycle: req}
  - {name: Spec, document: {contains: [Requirement]}, lifecycle: req, changeControlled: true}
linkTypes:
  - {name: contains, from: Spec, to: Requirement}
`
	d, err := methodology.ParseDomain([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	// protobuf round trip keeps the three parts
	back := DomainFromPB(DomainToPB(DomainRecord{Domain: *d, Status: StatusDraft}))
	if len(back.Lifecycles) != 1 || back.Lifecycles[0].Transitions[0].Requires.Attributes[0] != "title" || back.NodeTypes[1].Lifecycle != "req" || back.NodeTypes[1].Document == nil || len(back.LinkTypes) != 1 {
		t.Fatalf("pb round trip: %+v", back)
	}
	for name, mk := range stores(t) {
		t.Run(name, func(t *testing.T) {
			enf, _ := authz.NewCasbin(nil)
			s := &Service{Store: mk(t), Authz: enf}
			ctx := as("methodologist")
			if _, issues, err := s.SaveDomain(ctx, *d); err != nil || len(issues) > 0 {
				t.Fatalf("save: %v %v", err, issues)
			}
			got, err := s.GetDomain(ctx, "docs", "1.0.0")
			if err != nil || len(got.Domain.Lifecycles) != 1 || got.Domain.Lifecycles[0].Name != "req" || got.Domain.NodeTypes[0].Lifecycle != "req" {
				t.Fatalf("stored domain: %+v %v", got.Domain, err)
			}
			if l := got.Domain.LifecycleOf("Spec"); l == nil || !l.Editable("draft") || l.Editable("approved") {
				t.Fatalf("resolved lifecycle: %+v", l)
			}
			// a reference to a lifecycle the domain does not have is rejected
			bad := *d
			bad.Version = "1.0.1"
			bad.NodeTypes = []methodology.NodeType{{Name: "Requirement", Lifecycle: "nope"}}
			if _, issues, _ := s.SaveDomain(ctx, bad); len(issues) == 0 {
				t.Fatal("unknown lifecycle must be an issue")
			}
		})
	}
}

// A domain with algorithms survives the store and the wire format, and a broken
// plug or script is reported when the draft is saved.
func TestDomainAlgorithms(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "domains", "alm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := methodology.ParseDomain(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Algorithms) == 0 || len(d.Instances) == 0 {
		t.Fatal("alm.yaml declares algorithms")
	}
	// wire round trip
	back := DomainFromPB(DomainToPB(DomainRecord{Domain: *d}))
	if len(back.Algorithms) != len(d.Algorithms) || len(back.Instances) != len(d.Instances) {
		t.Fatalf("wire round trip lost algorithms: %d/%d", len(back.Algorithms), len(back.Instances))
	}
	if got, want := back.Instances[2].Values["pattern"], d.Instances[2].Values["pattern"]; got != want {
		t.Fatalf("instance values: %v != %v", got, want)
	}
	if back.NodeTypes[1].Validators[0].Instance != d.NodeTypes[1].Validators[0].Instance {
		t.Fatalf("validators lost: %+v", back.NodeTypes[1])
	}
	for name, mk := range stores(t) {
		t.Run(name, func(t *testing.T) {
			enf, _ := authz.NewCasbin(nil)
			s := &Service{Store: mk(t), Authz: enf}
			ctx := as("methodologist")
			if _, issues, err := s.SaveDomain(ctx, *d); err != nil || len(issues) > 0 {
				t.Fatalf("save: %v %v", issues, err)
			}
			got, err := s.GetDomain(ctx, d.Name, d.Version)
			if err != nil || len(got.Domain.Algorithms) != len(d.Algorithms) || got.Domain.Algorithms[0].Params[0].Type != "regex" {
				t.Fatalf("get: %v %+v", err, got.Domain.Algorithms)
			}
			// a broken plug and a broken script are reported with their path
			bad := *d
			bad.Version = "2"
			bad.Algorithms = append([]algo.Algorithm(nil), d.Algorithms...)
			bad.Algorithms[0].Code = "function ("
			bad.Instances = append([]algo.Instance(nil), d.Instances...)
			bad.Instances[1].Algorithm = "missing"
			_, issues, err := s.SaveDomain(ctx, bad)
			if err != nil || len(issues) < 2 {
				t.Fatalf("expected issues: %v %v", issues, err)
			}
			text := issues.Error()
			if !strings.Contains(text, "algorithms[0].code") || !strings.Contains(text, `unknown algorithm "missing"`) {
				t.Fatalf("issues: %s", text)
			}
		})
	}
}

func TestRunAlgorithm(t *testing.T) {
	enf, _ := authz.NewCasbin(nil)
	s := &Service{Store: NewMemoryStore(), Authz: enf}
	a := algo.Algorithm{Name: "len", Type: algo.UsagePropertyValidator, Language: "javascript",
		Params: []algo.Param{{Name: "max", Type: "number", Required: true}},
		Code:   `if (String(ctx.value()).length > ctx.param("max")) ctx.fail("too long")`}
	out, err := s.RunAlgorithm(as("methodologist"), a, map[string]any{"max": 3.0}, map[string]any{"property": "p", "value": "abcd"})
	if err != nil || out.OK() || out.Failures[0] != "too long" {
		t.Fatalf("%v %+v", err, out)
	}
	if _, err := s.RunAlgorithm(as("methodologist"), a, map[string]any{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing required param: %v", err)
	}
	if _, err := s.RunAlgorithm(as("contributor"), a, map[string]any{"max": 3.0}, nil); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("contributor: %v", err)
	}
}
