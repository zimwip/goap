package criticality

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/risk"
	"github.com/zimwip/goap/pkg/verify"
)

func TestLevels(t *testing.T) {
	if Of(nil) != C2 || Of(map[string]any{domain.DataCriticality: "C3"}) != C3 || Of(map[string]any{domain.DataCriticality: "high"}) != C2 {
		t.Fatal("Of: a change that names no level is C2")
	}
	if !Lowers(C3, C1) || Lowers(C1, C3) || Lowers(C2, C2) {
		t.Fatal("Lowers")
	}
	d := Defaults()
	if !d[C1].Accepts("model") || d[C3].Accepts("tool") || !d[C3].Accepts("human") || d[C3].SignatoryRole == "" || d[C1].SignatoryRole != "" {
		t.Fatalf("the table is loose at C1 and strict at C3: %+v", d)
	}
	if d[C1].MaxDerogation <= d[C2].MaxDerogation || d[C2].MaxDerogation <= d[C3].MaxDerogation {
		t.Fatal("the stricter the level, the shorter a derogation")
	}
	if m := d[C3].Map(); m["signatoryRole"] != "derogation_signatory" || m["maxDerogationHours"] != int64(7*24) || len(m["oracles"].([]any)) != 1 {
		t.Fatalf("map: %v", m)
	}
}

func TestItemPolicy(t *testing.T) {
	verify.Register()
	risk.Register()
	check := ItemPolicy(nil)
	ctx := context.Background()
	change := func(l Level) domain.Change {
		return domain.Change{Data: map[string]any{domain.DataCriticality: string(l)}}
	}
	produced := func(oracle string) domain.ChangeItem {
		return domain.ChangeItem{Kind: verify.KindVerification, Data: map[string]any{verify.KeyState: verify.Produced, verify.KeyAction: "a", verify.KeyOracle: oracle}}
	}
	if err := check(ctx, change(C1), produced("model")); err != nil {
		t.Errorf("C1 accepts a model: %v", err)
	}
	if err := check(ctx, change(C3), produced("model")); err == nil || !strings.Contains(err.Error(), "C3") {
		t.Errorf("C3 refuses a model oracle: %v", err)
	}
	if err := check(ctx, change(C3), produced("")); err != nil {
		t.Errorf("an effect with no declared oracle is not constrained: %v", err)
	}
	verified := domain.ChangeItem{Kind: verify.KindVerification, Data: map[string]any{verify.KeyState: verify.Verified, verify.KeyOracle: "model"}}
	if err := check(ctx, change(C3), verified); err != nil {
		t.Errorf("only the produced entry names the oracle: %v", err)
	}
	drg := func(d time.Duration, status string) domain.ChangeItem {
		data := map[string]any{"expires": time.Now().Add(d).UTC().Format(time.RFC3339)}
		if status != "" {
			data["status"] = status
		}
		return domain.ChangeItem{Kind: risk.KindDerogation, Data: data}
	}
	if err := check(ctx, change(C3), drg(24*time.Hour, "")); err != nil {
		t.Errorf("a day at C3: %v", err)
	}
	if err := check(ctx, change(C3), drg(30*24*time.Hour, "")); err == nil {
		t.Error("a month at C3 is refused")
	}
	if err := check(ctx, change(C1), drg(30*24*time.Hour-time.Hour, "")); err != nil {
		t.Errorf("a month less an hour at C1: %v", err)
	}
	if err := check(ctx, change(C3), drg(-time.Hour, "closed")); err != nil {
		t.Errorf("a closing version is not bounded: %v", err)
	}
	// the resolver of the organisation replaces the table
	loose := ItemPolicy(func(context.Context, domain.Change, Level) Policy { return Policy{} })
	if err := loose(ctx, change(C3), produced("model")); err != nil {
		t.Errorf("a policy that names no oracle accepts any: %v", err)
	}
}
