package modelgw

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	modelv1 "github.com/zimwip/goap/gen/goap/model/v1"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/llm"
)

// stubModel answers every call with a fixed text and records the request.
type stubModel struct {
	text    string
	aliases []AliasEntry
	got     []llm.Request
	metas   []llm.CallMeta
	err     error
}

func (m *stubModel) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	m.got = append(m.got, req)
	m.metas = append(m.metas, llm.MetaFrom(ctx))
	return llm.Response{Text: m.text, Usage: llm.Usage{InputTokens: 7, OutputTokens: 3}}, m.err
}

func (m *stubModel) Available(context.Context) ([]ModelEntry, []AliasEntry, error) {
	return nil, m.aliases, nil
}

func helperAliases() []AliasEntry { return []AliasEntry{{Alias: HelperAlias, Target: "fake/echo"}} }

func fields() []SuggestField {
	return []SuggestField{
		{ID: "title", Type: "string"},
		{ID: "size", Type: "number"},
		{ID: "done", Type: "boolean"},
		{ID: "due", Type: "date"},
		{ID: "status", Type: "enum", EnumValues: []string{"open", "closed"}},
		{ID: "extra", Type: "json"},
		{ID: "key", Type: "string", ReadOnly: true},
	}
}

func input() SuggestInput {
	return SuggestInput{Context: SuggestContext{TabKind: "node", Fields: fields()}}
}

func ids(r SuggestResult) []string {
	var out []string
	for _, p := range r.Proposals {
		out = append(out, p.FieldID)
	}
	return out
}

func TestSuggestParsesAndFilters(t *testing.T) {
	answer := "Here you go:\n```json\n" + `{"message":"ok","proposals":[
	 {"fieldId":"title","value":"A title","rationale":"r"},
	 {"fieldId":"title","value":"duplicate"},
	 {"fieldId":"nope","value":"x"},
	 {"fieldId":"key","value":"readonly"},
	 {"fieldId":"size","value":"12"},
	 {"fieldId":"done","value":true},
	 {"fieldId":"due","value":"2026-10-07"},
	 {"fieldId":"status","value":"pending"},
	 {"fieldId":"extra","value":{"a":[1]}},
	 {"fieldId":"size"}
	]}` + "\n```"
	m := &stubModel{text: answer, aliases: helperAliases()}
	r, err := Suggest(context.Background(), m, input())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids(r), ","); got != "title,done,due,extra" {
		t.Fatalf("kept %s", got)
	}
	if string(r.Proposals[0].Value) != `"A title"` || r.Proposals[0].Rationale != "r" || r.Message != "ok" {
		t.Fatalf("unexpected %+v", r)
	}
	if r.Usage.InputTokens != 7 {
		t.Fatalf("usage %+v", r.Usage)
	}
	req := m.got[0]
	if req.Model != HelperAlias || !req.JSON || len(req.Messages) != 1 || req.Messages[0].Role != "user" || !strings.Contains(req.System, `"id":"title"`) {
		t.Fatalf("request %+v", req)
	}
}

func TestSuggestEnumAndNonJSONAnswer(t *testing.T) {
	m := &stubModel{text: `{"proposals":[{"fieldId":"status","value":"closed"}]}`, aliases: helperAliases()}
	r, err := Suggest(context.Background(), m, input())
	if err != nil || len(r.Proposals) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	m.text = "I cannot help with that."
	r, err = Suggest(context.Background(), m, input())
	if err != nil || len(r.Proposals) != 0 || r.Message != "I cannot help with that." {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestSuggestMessages(t *testing.T) {
	in := input()
	in.Messages = []SuggestMessage{{Role: "assistant", Text: "dropped: first turn is not the user's"}, {Role: "user", Text: "a"}, {Role: "assistant", Text: "b"}}
	in.Instruction = "shorter"
	m := &stubModel{text: "{}", aliases: helperAliases()}
	if _, err := Suggest(context.Background(), m, in); err != nil {
		t.Fatal(err)
	}
	msgs := m.got[0].Messages
	if len(msgs) != 3 || msgs[0].Content != "a" || msgs[2].Role != "user" || msgs[2].Content != "shorter" {
		t.Fatalf("messages %+v", msgs)
	}
}

func TestSuggestLimits(t *testing.T) {
	m := &stubModel{text: "{}", aliases: helperAliases()}
	cases := map[string]func(*SuggestInput){
		"no field": func(in *SuggestInput) { in.Context.Fields = nil },
		"too many": func(in *SuggestInput) {
			in.Context.Fields = nil
			for i := 0; i <= MaxSuggestFields; i++ {
				in.Context.Fields = append(in.Context.Fields, SuggestField{ID: string(rune('a'+i%26)) + strings.Repeat("x", i)})
			}
		},
		"duplicate id":  func(in *SuggestInput) { in.Context.Fields = append(in.Context.Fields, SuggestField{ID: "title"}) },
		"context bytes": func(in *SuggestInput) { in.Context.Selection = strings.Repeat("x", MaxSuggestContextBytes+1) },
		"many messages": func(in *SuggestInput) {
			in.Messages = make([]SuggestMessage, MaxSuggestMessages+1)
			for i := range in.Messages {
				in.Messages[i] = SuggestMessage{Role: "user", Text: "x"}
			}
		},
		"bad role": func(in *SuggestInput) { in.Messages = []SuggestMessage{{Role: "system", Text: "x"}} },
		"long message": func(in *SuggestInput) {
			in.Messages = []SuggestMessage{{Role: "user", Text: strings.Repeat("x", MaxSuggestMessageBytes+1)}}
		},
		"instruction": func(in *SuggestInput) { in.Instruction = strings.Repeat("x", MaxSuggestMessageBytes+1) },
	}
	for name, mut := range cases {
		in := input()
		mut(&in)
		if _, err := Suggest(context.Background(), m, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	if len(m.got) != 0 {
		t.Fatal("the model must not be called for an invalid request")
	}
}

func TestSuggestRefusedWithoutHelperAlias(t *testing.T) {
	m := &stubModel{text: "{}", aliases: []AliasEntry{{Alias: "default", Target: "fake/echo"}}}
	_, err := Suggest(context.Background(), m, input())
	if !errors.Is(err, ErrModelDisabled) || len(m.got) != 0 {
		t.Fatalf("%v", err)
	}
	if code := connect.CodeOf(rpcErr(err)); code != connect.CodeFailedPrecondition {
		t.Fatalf("code %v", code)
	}
}

// The model call goes through the gateway as the caller: the role allow-list of the model applies.
func TestSuggestAuthorization(t *testing.T) {
	ctx := context.Background()
	svc, g := newService(NewMemoryStore())
	echo := ModelEntry{Provider: "fake", Model: "echo", Enabled: true, Roles: []string{"methodologist"}}
	seed(t, g, []ProviderRecord{{Name: "fake", Kind: "fake", Protocol: "fake", Enabled: true}}, []ModelEntry{echo}, []AliasEntry{{Alias: HelperAlias, Target: "fake/echo"}})
	h := &Handler{Service: svc}
	call := func(roles string) (*connect.Response[modelv1.SuggestResponse], error) {
		req := connect.NewRequest(&modelv1.SuggestRequest{Context: &modelv1.SuggestContext{Fields: []*modelv1.SuggestField{{Id: "title", Type: "string"}}}})
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderRoles, roles)
		return h.Suggest(ctx, req)
	}
	if _, err := call("contributor"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a caller without the role cannot use the helper model: %v", err)
	}
	resp, err := call("methodologist")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Message != "" || len(resp.Msg.Proposals) != 0 || resp.Msg.Usage.InputTokens == 0 { // the fake model answers JSON with no proposal, and reports usage
		t.Fatalf("%+v", resp.Msg)
	}
	// the service itself applies the catalog policy too
	user := authz.With(ctx, authz.Principal{Subject: "u", Roles: []string{"contributor"}})
	if _, err := svc.Complete(user, llm.Request{Model: HelperAlias, Messages: []llm.Message{{Role: "user", Content: "x"}}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("%v", err)
	}
}

// The helper declares itself to the ledger of the gateway (ADR 0089).
func TestSuggestStampsItsSource(t *testing.T) {
	m := &stubModel{text: `{"message":"ok"}`, aliases: helperAliases()}
	if _, err := Suggest(context.Background(), m, input()); err != nil {
		t.Fatal(err)
	}
	if len(m.metas) != 1 || m.metas[0].Source != llm.SourceHelper {
		t.Fatalf("%+v", m.metas)
	}
}
