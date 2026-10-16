package registrysvc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/def"
)

// tickingClock gives every write its own instant, so that "published last" is never a tie.
func tickingClock() func() time.Time {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time { t = t.Add(time.Second); return t }
}

func versionedService(t *testing.T) *Service {
	t.Helper()
	enf, _ := authz.NewCasbin(nil)
	return &Service{Store: NewGraphStore(graph.New(graph.NewMemory())), Authz: enf, now: tickingClock()}
}

// A new change takes the latest published version of its methodology, a change keeps the version it names (ADR 0113).
func TestMethodologyVersionOfAChange(t *testing.T) {
	s := versionedService(t)
	ctx := as("admin")
	if v, err := s.MethodologyVersion(ctx, "uses"); err != nil || v != "" {
		t.Fatalf("unknown methodology: %q %v", v, err)
	}
	if _, _, err := domainsOf(s).ImportDomain(ctx, mustYAML(t, almDomain("1")), true); err != nil {
		t.Fatal(err)
	}
	m := refMeth("uses")
	if _, _, err := s.importPublished(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.Version = "2"
	m.Description = "second"
	if _, _, err := s.importPublished(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.Version = "3"
	if _, _, err := s.Save(ctx, m); err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]string{"uses": "2", "uses@1": "1", "uses@2": "2"} {
		if v, err := s.MethodologyVersion(ctx, ref); err != nil || v != want {
			t.Fatalf("%s: %q %v, want %s", ref, v, err, want)
		}
	}
	if _, err := s.MethodologyVersion(ctx, "uses@3"); !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("a change never follows a draft: %v", err)
	}
	if _, err := s.MethodologyVersion(ctx, "uses@9"); !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("unknown version: %v", err)
	}
	// the readers of a methodology take the reference
	if c, err := s.Methodology(ctx, "uses@1"); err != nil || c.Version != "1" {
		t.Fatalf("compiled at version 1: %v", err)
	}
	if c, err := s.Methodology(ctx, "uses"); err != nil || c.Version != "2" {
		t.Fatalf("compiled at the latest: %v", err)
	}
}

// Every start publishes the versions of the shipped files not stored yet: a new version of a domain that a published
// methodology could not follow is loaded anyway, the methodology being suspended (ADR 0125 §3), and a new version of
// the methodology becomes the one new changes take (ADR 0113).
func TestSeedLoadsNewVersions(t *testing.T) {
	s := versionedService(t)
	ctx := as("admin")
	ddir, mdir := t.TempDir(), t.TempDir()
	write := func(dir, name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(ddir, "alm.yaml", mustYAML(t, almDomain("1")))
	m := refMeth("uses")
	src, err := m.YAML()
	if err != nil {
		t.Fatal(err)
	}
	write(mdir, "uses.yaml", src)
	seed := func(wantD, wantM int) {
		t.Helper()
		ds, err := domainsOf(s).SeedDomains(ctx, ddir)
		if err != nil || len(ds) != wantD {
			t.Fatalf("domains: %v %v", ds, err)
		}
		ms, err := s.Seed(ctx, mdir)
		if err != nil || len(ms) != wantM {
			t.Fatalf("methodologies: %v %v", ms, err)
		}
	}
	seed(1, 1)
	seed(0, 0) // nothing new: nothing loaded

	// alm@2 drops the link type uses@1 relies on, uses@2 no longer needs it
	v2 := almDomain("2")
	v2.LinkTypes = nil
	v2.NodeTypes = append(v2.NodeTypes, def.NodeType{Name: "Need"})
	write(ddir, "alm.yaml", mustYAML(t, v2))
	m2 := refMeth("uses")
	m2.Version = "2"
	m2.Conditions = []methodology.Condition{{Name: "c", Expr: `changeImpacts.exists(n, n.hasPost && n.post.type == "alm@Need")`}}
	src2, err := m2.YAML()
	if err != nil {
		t.Fatal(err)
	}
	write(mdir, "uses.yaml", src2)
	seed(1, 1)

	if v, _ := s.MethodologyVersion(ctx, "uses"); v != "2" {
		t.Fatalf("new changes take the new version: %q", v)
	}
	if _, err := s.Methodology(ctx, "uses@1"); !errors.Is(err, ErrSuspended) {
		t.Fatalf("version 1 relies on a link type alm@2 dropped: suspended: %v", err)
	}
	if _, err := s.MethodologyVersion(ctx, "uses@1"); !errors.Is(err, graph.ErrInvalid) || !errors.Is(err, ErrSuspended) {
		t.Fatalf("no change starts on a suspended version: %v", err)
	}
	cur, err := s.Methodology(ctx, "uses")
	if err != nil || !cur.Types.HasNodeType("alm@Need") {
		t.Fatalf("version 2 is written against alm@2: %v", err)
	}
	if r, _ := s.Get(ctx, "uses", "2"); r.Methodology.Domains["alm"] != "2" {
		t.Fatalf("pins: %v", r.Methodology.Domains)
	}
}

// A methodology published before the pins existed follows the domain versions in force when it was published, while
// the domains in force do not break it.
func TestUnpinnedPublishedMethodologyFollowsItsTime(t *testing.T) {
	s := versionedService(t)
	ctx := as("admin")
	if _, _, err := domainsOf(s).ImportDomain(ctx, mustYAML(t, almDomain("1")), true); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC() // the clock of the domain registry
	if err := s.Store.Save(ctx, Record{Methodology: refMeth("legacy"), Status: StatusPublished, CreatedAt: at, UpdatedAt: at, PublishedAt: at}); err != nil {
		t.Fatal(err)
	}
	v2 := almDomain("2")
	v2.NodeTypes = append(v2.NodeTypes, def.NodeType{Name: "Need"})
	if _, _, err := domainsOf(s).ImportDomain(ctx, mustYAML(t, v2), true); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Methodology(ctx, "legacy"); err != nil || !c.Types.HasLinkType("alm@verifies") || c.Types.HasNodeType("alm@Need") {
		t.Fatalf("compiled against alm@1: %v", err)
	}
	v3 := almDomain("3")
	v3.LinkTypes = nil
	if _, _, err := domainsOf(s).ImportDomain(ctx, mustYAML(t, v3), true); err != nil {
		t.Fatalf("a domain is published whatever the methodologies: %v", err)
	}
	if _, err := s.Methodology(ctx, "legacy"); !errors.Is(err, ErrSuspended) {
		t.Fatalf("alm@3 breaks it: suspended: %v", err)
	}
}

// A draft pins only what it declares; a pin must name a published version of a stored domain.
func TestDomainPins(t *testing.T) {
	s := versionedService(t)
	ctx := as("admin")
	if _, _, err := domainsOf(s).ImportDomain(ctx, mustYAML(t, almDomain("1")), true); err != nil {
		t.Fatal(err)
	}
	m := refMeth("pins")
	m.Domains = map[string]string{"alm": "7"}
	if _, issues, err := s.Save(ctx, m); err != nil || len(issues) == 0 || issues[0].Path != "domains.alm" {
		t.Fatalf("unknown pinned version: %v %v", issues, err)
	}
	m.Domains = map[string]string{"organisation": "1"}
	if _, issues, err := s.Save(ctx, m); err != nil || len(issues) == 0 || issues[0].Path != "domains.organisation" {
		t.Fatalf("a built-in domain is not pinned: %v %v", issues, err)
	}
	m.Domains = map[string]string{"alm": "1"}
	if _, issues, err := s.Save(ctx, m); err != nil || issues.HasErrors() {
		t.Fatalf("pinned on alm@1: %v %v", issues, err)
	}
}

func mustYAML(t *testing.T, d def.Domain) []byte {
	t.Helper()
	b, err := d.YAML()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
