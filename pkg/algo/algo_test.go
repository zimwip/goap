package algo

import "testing"

// A "platform@<name>" algorithm reference resolves against Set.Platform instead of the local
// Algorithms (ADR 0041); a bare name keeps resolving locally.
func TestBindPlatformReference(t *testing.T) {
	platform := Set{Algorithms: []Algorithm{{Name: "shared", Type: UsagePropertyValidator, Language: JavaScript, Code: "x"}}}
	local := Set{
		Algorithms: []Algorithm{{Name: "own", Type: UsagePropertyValidator, Language: JavaScript, Code: "y"}},
		Instances: []Instance{
			{Name: "local-inst", Algorithm: "own"},
			{Name: "shared-inst", Algorithm: "platform@shared"},
			{Name: "unknown-inst", Algorithm: "platform@nope"},
		},
		Platform: &platform,
	}
	if b, err := local.Bind("local-inst", UsagePropertyValidator); err != nil || b.Code != "y" {
		t.Fatalf("bare name resolves locally: %+v %v", b, err)
	}
	if b, err := local.Bind("shared-inst", UsagePropertyValidator); err != nil || b.Code != "x" {
		t.Fatalf("platform@ reference resolves against Platform: %+v %v", b, err)
	}
	if _, err := local.Bind("unknown-inst", UsagePropertyValidator); err == nil {
		t.Fatal("an unknown platform algorithm must fail to bind")
	}
	local.Platform = nil
	if _, err := local.Bind("shared-inst", UsagePropertyValidator); err == nil {
		t.Fatal("a platform@ reference must fail to bind without a Platform set")
	}
}

func TestPlatformRef(t *testing.T) {
	if name, ok := PlatformRef("platform@regex-match"); !ok || name != "regex-match" {
		t.Fatalf("got %q, %v", name, ok)
	}
	if _, ok := PlatformRef("regex-match"); ok {
		t.Fatal("a bare name is not a platform reference")
	}
	if _, ok := PlatformRef("alm@regex-match"); ok {
		t.Fatal("only the platform namespace is a platform reference")
	}
}
