package llmcfg

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func node(key, typ string, props map[string]any) domain.Node {
	return domain.Node{Namespace: NamespacePlatform, Key: key, Type: typ, Properties: props}
}

func TestPropsRoundTrip(t *testing.T) {
	m := Model{Provider: "mistral", Model: "large", Enabled: true, QuotaTokens: 1000, QuotaPeriod: PeriodDay, Roles: []string{"methodologist"}}
	got, err := ModelFromProps(m.Props())
	if err != nil || got.QuotaTokens != 1000 || got.QuotaPeriod != PeriodDay || len(got.Roles) != 1 || got.DisplayName != "large" {
		t.Fatalf("%+v %v", got, err)
	}
	p := Provider{Name: "mistral", Kind: "mistral", Protocol: "openai", Enabled: true, APIKeyRef: "env:K"}
	if back, err := ProviderFromProps(p.Props()); err != nil || back != p {
		t.Fatalf("%+v %v", back, err)
	}
}

func TestSnapshotIgnoresDanglingAndMalformedNodes(t *testing.T) {
	prov := Provider{Name: "fake", Kind: "fake", Protocol: "fake", Enabled: true}
	ok := Model{Provider: "fake", Model: "echo", Enabled: true}
	orphan := Model{Provider: "gone", Model: "x", Enabled: true}
	s := BuildSnapshot("b1", []domain.Node{
		node(ProviderKey("fake"), NodeTypeProvider, prov.Props()),
		node(ok.Key(), NodeTypeModel, ok.Props()),
		node(orphan.Key(), NodeTypeModel, orphan.Props()),
		node(AliasKey("default"), NodeTypeAlias, Alias{Alias: "default", Target: "fake/echo"}.Props()),
		node(AliasKey("lost"), NodeTypeAlias, Alias{Alias: "lost", Target: "fake/none"}.Props()),
		node(ProviderKey("Bad Name"), NodeTypeProvider, map[string]any{"name": "Bad Name", "protocol": "fake"}),
	}, nil)
	if len(s.Providers) != 1 || len(s.Models) != 1 || len(s.Aliases) != 1 || len(s.Problems) != 3 {
		t.Fatalf("%+v", s)
	}
}
