package engine

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// MayRunAgent is the rule Start applies to its initiator, answered without starting anything (ADR 0090).
func TestMayRunAgentIsTheRuleOfStart(t *testing.T) {
	m, err := methodology.Parse([]byte(rolesYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e := scopeEngine(mustCasbin(t))
	e.Methodologies = StaticMethodologies{"roled": c}
	ctx := func(p authz.Principal) context.Context { return authz.With(context.Background(), p) }
	dev := authz.Principal{Subject: "bob", Roles: []string{"developer"}}
	for _, tc := range []struct {
		agent string
		want  bool
		roles int
	}{{"shipper", true, 2}, {"auditing", false, 1}} {
		ok, roles, err := e.MayRunAgent(ctx(dev), "roled", tc.agent, "PROJ-X")
		if err != nil || ok != tc.want || len(roles) != tc.roles {
			t.Errorf("%s: %v %v %v", tc.agent, ok, roles, err)
		}
	}
	if _, _, err := e.MayRunAgent(ctx(dev), "roled", "nobody", "PROJ-X"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("unknown agent: %v", err)
	}
	if _, _, err := e.MayRunAgent(ctx(dev), "nope", "shipper", "PROJ-X"); err == nil {
		t.Fatal("unknown methodology")
	}
}

// A process started for a conversation of the assistant carries it to the ledger of the gateway (ADR 0090).
func TestConversationOfAProcessReachesTheLedgerMeta(t *testing.T) {
	ctx := context.Background()
	e, _, base := setup(t)
	inner := scripted(t)
	var mu sync.Mutex
	var metas []llm.CallMeta
	e.Executors[methodology.KindLLM] = LLMExecutor{Client: llm.ClientFunc(func(ctx context.Context, req llm.Request) (llm.Response, error) {
		mu.Lock()
		metas = append(metas, llm.MetaFrom(ctx))
		mu.Unlock()
		return inner.Complete(ctx, req)
	})}
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Intent: "The PSP changes its API, what does this break?",
		ProjectID: testProject, Vars: map[string]any{VarConversation: "CONV-7"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Run(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if len(metas) == 0 {
		t.Fatal("no call")
	}
	for _, m := range metas {
		if m.ConversationID != "CONV-7" || m.ProcessID != p.ID {
			t.Fatalf("meta %+v", m)
		}
	}
}
