package modelgw

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	modelv1 "github.com/zimwip/goap/gen/goap/model/v1"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/llm"
)

// A call a platform service makes on behalf of a person is attributed to that person (ADR 0114): it is in their
// consumption next to their own calls, with its exchange, and in nobody else's; a person never acts on behalf of
// another, whatever their request says.
func TestLedgerAttributesToTheEndUser(t *testing.T) {
	svc, _ := ledgerService(t, NewMemoryStore())
	c, rpc := rpcClient(t, svc, adminOnly{})
	as := func(p authz.Principal) context.Context {
		return llm.WithMeta(authz.With(context.Background(), p), llm.CallMeta{Source: llm.SourceHelper})
	}
	trigger := authz.System("trigger:nightly").For("alice")
	for _, p := range []authz.Principal{{Subject: "alice"}, trigger, {Subject: "bob", OnBehalfOf: "alice"}, authz.System("indexer")} {
		if _, err := c.Complete(as(p), llm.Request{Messages: []llm.Message{{Role: "user", Content: "hi"}}}); err != nil {
			t.Fatal(err)
		}
	}
	all := calls(t, svc, UsageFilter{})
	want := [][2]string{{"alice", "alice"}, {"system:trigger:nightly", "alice"}, {"bob", "bob"}, {"system:indexer", ""}}
	if len(all) != len(want) {
		t.Fatalf("%+v", all)
	}
	for i, w := range want {
		if all[i].Subject != w[0] || all[i].EndUser != w[1] {
			t.Fatalf("row %d: subject %q end user %q, want %v", i, all[i].Subject, all[i].EndUser, w)
		}
	}

	who := func(sub string) context.Context {
		return authz.With(context.Background(), authz.Principal{Subject: sub})
	}
	list := func(ctx context.Context, f *modelv1.UsageFilter) ([]*modelv1.LLMCall, error) {
		r, err := rpc.ListUsage(ctx, connect.NewRequest(&modelv1.ListUsageRequest{Filter: f}))
		if err != nil {
			return nil, err
		}
		return r.Msg.Calls, nil
	}
	if got, err := list(who("alice"), nil); err != nil || len(got) != 2 || got[1].Subject != "system:trigger:nightly" || got[1].EndUser != "alice" {
		t.Fatalf("alice reads her calls and the ones made on her behalf: %v %+v", err, got)
	}
	if got, err := list(who("alice"), &modelv1.UsageFilter{Subject: "alice"}); err != nil || len(got) != 1 {
		t.Fatalf("her own calls only: %v %+v", err, got)
	}
	if got, err := list(who("alice"), &modelv1.UsageFilter{Subject: "system:trigger:nightly"}); err != nil || len(got) != 1 {
		t.Fatalf("the calls of a service on her behalf: %v %+v", err, got)
	}
	if _, err := list(who("alice"), &modelv1.UsageFilter{EndUser: "bob"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("another person's consumption is refused: %v", err)
	}
	if got, err := list(who("bob"), nil); err != nil || len(got) != 1 {
		t.Fatalf("bob reads his own call only: %v %+v", err, got)
	}
	r, err := rpc.UsageSummary(who("root"), connect.NewRequest(&modelv1.UsageSummaryRequest{GroupBy: GroupEndUser}))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]int64{}
	for _, row := range r.Msg.Rows {
		by[row.Key] = row.Calls
	}
	if by["alice"] != 2 || by["bob"] != 1 || by[""] != 1 {
		t.Fatalf("summary by end user: %v", by)
	}
	// the exchange of a call made on her behalf is hers to read
	if _, _, err := svc.CallExchange(who("alice"), all[1].Seq, false); err != nil {
		t.Fatalf("alice reads the exchange of a call made on her behalf: %v", err)
	}
	if _, _, err := svc.CallExchange(who("bob"), all[1].Seq, false); err == nil {
		t.Fatal("bob does not read it")
	}
}
