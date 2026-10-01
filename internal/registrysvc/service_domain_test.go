package registrysvc

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

func almDomain(version string) methodology.Domain {
	return methodology.Domain{Name: "alm", Version: version, Schema: methodology.Schema{
		NodeTypes: []methodology.NodeType{{Name: "Requirement"}, {Name: "TestCase", Properties: []string{"title"}}},
		LinkTypes: []methodology.LinkType{{Name: "verifies", From: "TestCase", To: "Requirement"}},
	}}
}

func refMeth(name string) methodology.Methodology {
	return methodology.Methodology{
		Name: name, Version: "1", Namespace: "alm",
		Conditions: []methodology.Condition{{Name: "c", Expr: `changeImpacts.exists(n, n.hasPost && n.post.out.exists(l, l.type == "alm@verifies"))`}},
		Actions:    []methodology.Action{{Name: "a", Kind: methodology.KindHuman, Effects: map[string]bool{"c": true}}},
		Goals:      []methodology.Goal{{Name: "g", Pre: map[string]bool{"c": true}}},
	}
}

func TestDomainLifecycle(t *testing.T) {
	for name, mk := range domainStores(t) {
		t.Run(name, func(t *testing.T) {
			enf, _ := authz.NewCasbin(nil)
			s := &Service{Store: NewGraphStore(graph.New(graph.NewMemory())), DomainStore: mk(t), Authz: enf}
			ctx := as("admin")

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

			// a methodology is validated against the published domains: a draft one does not count
			if _, issues, err := s.Save(ctx, refMeth("uses")); err != nil || len(issues) == 0 {
				t.Fatalf("no published alm domain yet: %v %v", issues, err)
			}
			if _, err := s.Publish(ctx, "uses", "1"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("publish on a draft domain: %v", err)
			}
			if _, err := s.PublishDomain(ctx, "alm", "1"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Publish(ctx, "uses", "1"); err != nil {
				t.Fatalf("publish methodology: %v", err)
			}
			if c, err := s.Methodology(ctx, "uses"); err != nil || c.Types == nil || !c.Types.HasLinkType("alm@verifies") {
				t.Fatalf("the compiled methodology sees the types in force: %v", err)
			}
			if cat, err := s.Types(ctx); err != nil || !cat.HasNodeType("alm@Requirement") || !cat.HasNodeType("methodology@Agent") {
				t.Fatalf("type catalogue: %v", err)
			}

			// published versions are immutable; the version in force cannot be archived while used
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

			// a new version that drops a type a published methodology uses is refused
			v2, err := s.CreateDomainVersion(ctx, "alm", "1", "2")
			if err != nil || v2.Status != StatusDraft {
				t.Fatalf("new version: %+v %v", v2, err)
			}
			d := v2.Domain
			d.LinkTypes = nil
			if _, _, err := s.SaveDomain(ctx, d); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PublishDomain(ctx, "alm", "2"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "would break") {
				t.Fatalf("breaking domain version: %v", err)
			}
			// a compatible one becomes the version in force; the former one can then be archived
			v3 := almDomain("3")
			v3.NodeTypes = append(v3.NodeTypes, methodology.NodeType{Name: "Need"})
			if _, _, err := s.SaveDomain(ctx, v3); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PublishDomain(ctx, "alm", "3"); err != nil {
				t.Fatal(err)
			}
			if cat, _ := s.Types(ctx); !cat.HasNodeType("alm@Need") || cat.Domains()["alm"] != "3" {
				t.Fatal("the latest published version is in force")
			}
			if err := s.DeleteDomain(ctx, "alm", "1"); err != nil {
				t.Fatalf("a version no longer in force is archived: %v", err)
			}

			// drafts are deletable, lists show the latest published per name
			if err := s.DeleteDomain(ctx, "alm", "2"); err != nil {
				t.Fatal(err)
			}
			list, err := s.DomainVersions(ctx, false)
			if err != nil {
				t.Fatal(err)
			}
			var builtin []string
			stored := slices.DeleteFunc(list, func(r DomainRecord) bool {
				if r.Builtin {
					builtin = append(builtin, r.Domain.Name)
				}
				return r.Builtin
			})
			if len(stored) != 1 || stored[0].Domain.Version != "3" || !slices.Equal(builtin, []string{"methodology", "organisation", "platform"}) {
				t.Fatalf("latest: %v %+v", builtin, stored)
			}
		})
	}
}

func TestMethodologyNamespaceAndTypes(t *testing.T) {
	enf, _ := authz.NewCasbin(nil)
	s := &Service{Store: NewMemoryStore(), Authz: enf}
	ctx := as("admin")
	if _, _, err := s.ImportDomain(ctx, []byte("name: alm\nversion: \"1\"\nnodeTypes: [Requirement]\n"), true); err != nil {
		t.Fatal(err)
	}
	m := refMeth("nowhere")
	m.Namespace = "nope"
	_, issues, err := s.Save(ctx, m)
	if err != nil || !strings.Contains(issues.Error(), "no published domain nope") || !strings.Contains(issues.Error(), "unknown link type alm@verifies") {
		t.Fatalf("issues: %v %v", issues, err)
	}
	m.Namespace = ""
	if _, issues, _ := s.Save(ctx, m); len(issues) == 0 || issues[0].Path != "namespace" {
		t.Fatalf("a methodology names its target namespace: %v", issues)
	}
	for _, name := range []string{"methodology", "organisation", "platform"} {
		if _, _, err := s.SaveDomain(ctx, methodology.Domain{Name: name, Version: "9", Schema: methodology.Schema{NodeTypes: []methodology.NodeType{{Name: "X"}}}}); !errors.Is(err, ErrImmutable) {
			t.Fatalf("the built-in domain %s is frozen: %v", name, err)
		}
		if _, err := s.CreateDomainVersion(ctx, name, "", "9"); !errors.Is(err, ErrImmutable) {
			t.Fatalf("no new version of the frozen domain %s: %v", name, err)
		}
		if r, err := s.GetDomain(ctx, name, ""); err != nil || !r.Builtin || r.Status != StatusPublished {
			t.Fatalf("the built-in domain %s is readable: %+v %v", name, r, err)
		}
	}
	if err := s.DeleteDomain(ctx, "organisation", ""); !errors.Is(err, ErrImmutable) {
		t.Fatalf("a built-in domain cannot be archived: %v", err)
	}

	if _, issues, _ := s.SaveDomain(ctx, methodology.Domain{Name: "ext", Version: "1", Schema: methodology.Schema{NodeTypes: []methodology.NodeType{{Name: "X", Extends: "alm@Nope"}}}}); len(issues) == 0 {
		t.Fatal("a reference to an unknown type of another domain is reported")
	}
}

func TestDomainImportExport(t *testing.T) {
	enf, _ := authz.NewCasbin(nil)
	s := &Service{Store: NewMemoryStore(), Authz: enf}
	ctx := as("admin")
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
			ctx := as("admin")
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
			ctx := as("admin")
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
	out, err := s.RunAlgorithm(as("admin"), a, map[string]any{"max": 3.0}, map[string]any{"property": "p", "value": "abcd"})
	if err != nil || out.OK() || out.Failures[0] != "too long" {
		t.Fatalf("%v %+v", err, out)
	}
	if _, err := s.RunAlgorithm(as("admin"), a, map[string]any{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing required param: %v", err)
	}
	if _, err := s.RunAlgorithm(as("contributor"), a, map[string]any{"max": 3.0}, nil); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("contributor: %v", err)
	}
}

func TestDomainNodeTypeEditors(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "domains", "builtin", "platform.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := methodology.ParseDomain(src)
	if err != nil {
		t.Fatal(err)
	}
	if issues := d.Validate(); len(issues) > 0 {
		t.Fatal(issues)
	}
	// wire round trip
	back := DomainFromPB(DomainToPB(DomainRecord{Domain: *d}))
	editors := map[string]string{}
	for _, n := range back.NodeTypes {
		editors[n.Name] = n.Editor
	}
	for typ, want := range map[string]string{"MCP": "mcp", "AdapterDef": "adapter", "LlmProvider": ""} {
		if editors[typ] != want {
			t.Fatalf("editor of %s: %q, want %q", typ, editors[typ], want)
		}
	}
}

func TestListTypes(t *testing.T) {
	s := &Service{Store: NewMemoryStore()}
	withALM(t, s)
	res, err := (&Handler{Service: s}).ListTypes(as("contributor"), connect.NewRequest(&registryv1.ListTypesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	byRef := map[string]*registryv1.TypeInfo{}
	for _, ti := range res.Msg.Types {
		byRef[ti.Ref] = ti
	}
	sec := byRef["alm@SecurityRequirement"]
	if sec == nil || sec.Lifecycle.GetName() != "requirement" || len(sec.Ancestors) != 2 || byRef["methodology@Agent"].GetEditor() != "agent" {
		t.Fatalf("types: %+v", sec)
	}
	if res.Msg.Domains["alm"] == "" {
		t.Fatal("the version in force of each domain is listed")
	}
	found := false
	for _, l := range res.Msg.LinkTypes {
		found = found || (l.Ref == "alm@verifies" && l.From == "alm@TestCase" && l.To == "alm@Requirement")
	}
	if !found {
		t.Fatal("link types are listed with their ends")
	}
}
