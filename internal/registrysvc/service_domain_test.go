package registrysvc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
