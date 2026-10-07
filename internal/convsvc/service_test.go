package convsvc_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/protobuf/types/known/structpb"

	conversationsv1 "github.com/zimwip/goap/gen/goap/conversations/v1"
	"github.com/zimwip/goap/gen/goap/conversations/v1/conversationsv1connect"
	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/authz"
)

func stores(t *testing.T) map[string]convsvc.Store {
	t.Helper()
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := platform.MigrateSQLite(ctx, db, "conversations", convsvc.SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	all := map[string]convsvc.Store{"memory": convsvc.NewMemoryStore(), "sqlite": convsvc.SQLStore{DB: db}}
	// PostgreSQL when GOAP_TEST_PG_DSN is set
	if os.Getenv("GOAP_TEST_PG_DSN") != "" {
		all["postgres"] = convsvc.SQLStore{DB: stdlib.OpenDBFromPool(pgtest.Pool(t, convsvc.Migrations)), Dollar: true}
	}
	return all
}

// clock is a service clock that moves one second per reading, so that the order of the writes is the order of the times.
func clock() func() time.Time {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time { t = t.Add(time.Second); return t }
}

var (
	alice  = authz.Principal{Subject: "alice"}
	bob    = authz.Principal{Subject: "bob"}
	system = authz.System("assistant")
)

func newService(st convsvc.Store) *convsvc.Service {
	n := 0
	return &convsvc.Service{Store: st, Now: clock(), NewID: func() string { n++; return fmt.Sprintf("ID-%04d", n) }}
}

func TestOwnershipAndSystemAppend(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s := newService(st)
			c, err := s.Create(ctx, alice, "")
			if err != nil || c.Title != convsvc.DefaultTitle || c.Subject != "alice" {
				t.Fatalf("create = %+v, %v", c, err)
			}
			u, err := s.Append(ctx, alice, c.ID, "", convsvc.Content{Text: "open the change CHG-1"})
			if err != nil || u.Seq != 1 || u.Role != convsvc.RoleUser || u.Status != convsvc.StatusDone {
				t.Fatalf("user message = %+v, %v", u, err)
			}
			// a person does not write the assistant's words, a platform service does not write the user's
			if _, err := s.Append(ctx, alice, c.ID, convsvc.RoleAssistant, convsvc.Content{Text: "x"}); !errors.Is(err, convsvc.ErrForbidden) {
				t.Fatalf("assistant by owner: %v", err)
			}
			if _, err := s.Append(ctx, system, c.ID, convsvc.RoleUser, convsvc.Content{Text: "x"}); !errors.Is(err, convsvc.ErrNotFound) {
				t.Fatalf("user message by system: %v", err)
			}
			a, err := s.Append(ctx, system, c.ID, convsvc.RoleAssistant, convsvc.Content{Status: convsvc.StatusPending, ProcessID: "P1"})
			if err != nil || a.Seq != 2 || a.Status != convsvc.StatusPending || a.ProcessID != "P1" {
				t.Fatalf("assistant message = %+v, %v", a, err)
			}
			actions := []convsvc.Action{{"type": "open_change", "args": map[string]any{"id": "CHG-1"}}}
			up, err := s.UpdateMessage(ctx, system, a.ID, convsvc.Content{Text: "Opening it.", Actions: actions})
			if err != nil || up.Text != "Opening it." || up.Status != convsvc.StatusDone || up.ProcessID != "P1" {
				t.Fatalf("update = %+v, %v", up, err)
			}
			if _, err := s.UpdateMessage(ctx, alice, a.ID, convsvc.Content{Text: "forged"}); !errors.Is(err, convsvc.ErrForbidden) {
				t.Fatalf("update by owner: %v", err)
			}
			if _, err := s.UpdateMessage(ctx, system, u.ID, convsvc.Content{Text: "forged"}); !errors.Is(err, convsvc.ErrInvalid) {
				t.Fatalf("update of a user message: %v", err)
			}
			if _, err := s.UpdateMessage(ctx, system, "nope", convsvc.Content{}); !errors.Is(err, convsvc.ErrNotFound) {
				t.Fatalf("update of nothing: %v", err)
			}

			got, ms, err := s.Get(ctx, alice, c.ID)
			if err != nil || len(ms) != 2 || ms[0].ID != u.ID || ms[1].Text != "Opening it." || len(ms[1].Actions) != 1 || ms[1].Actions[0]["type"] != "open_change" {
				t.Fatalf("get = %+v, %+v, %v", got, ms, err)
			}
			if !got.UpdatedAt.After(c.UpdatedAt) {
				t.Fatalf("a message did not touch the conversation: %v then %v", c.UpdatedAt, got.UpdatedAt)
			}

			// bob and the platform see nothing of alice's conversation, and cannot change it
			for who, p := range map[string]authz.Principal{"bob": bob, "system": system} {
				if _, _, err := s.Get(ctx, p, c.ID); !errors.Is(err, convsvc.ErrNotFound) {
					t.Fatalf("%s get: %v", who, err)
				}
				if _, err := s.Rename(ctx, p, c.ID, "mine"); !errors.Is(err, convsvc.ErrNotFound) {
					t.Fatalf("%s rename: %v", who, err)
				}
				if err := s.Delete(ctx, p, c.ID); !errors.Is(err, convsvc.ErrNotFound) {
					t.Fatalf("%s delete: %v", who, err)
				}
			}
			if _, err := s.Append(ctx, bob, c.ID, "", convsvc.Content{Text: "hi"}); !errors.Is(err, convsvc.ErrNotFound) {
				t.Fatalf("bob append: %v", err)
			}
			if cs, _, err := s.List(ctx, bob, 0, ""); err != nil || len(cs) != 0 {
				t.Fatalf("bob list = %v, %v", cs, err)
			}
			if _, _, err := s.List(ctx, system, 0, ""); !errors.Is(err, convsvc.ErrForbidden) {
				t.Fatalf("system list: %v", err)
			}
			if _, err := s.Create(ctx, system, "x"); !errors.Is(err, convsvc.ErrForbidden) {
				t.Fatalf("system create: %v", err)
			}
			if _, err := s.Create(ctx, authz.Principal{}, "x"); !errors.Is(err, convsvc.ErrAnonymous) {
				t.Fatalf("anonymous create: %v", err)
			}

			r, err := s.Rename(ctx, alice, c.ID, " Release 2 ")
			if err != nil || r.Title != "Release 2" {
				t.Fatalf("rename = %+v, %v", r, err)
			}
			if _, err := s.Rename(ctx, alice, c.ID, " "); !errors.Is(err, convsvc.ErrInvalid) {
				t.Fatalf("empty title: %v", err)
			}
			if err := s.Delete(ctx, alice, c.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Get(ctx, alice, c.ID); !errors.Is(err, convsvc.ErrNotFound) {
				t.Fatalf("after delete: %v", err)
			}
			if _, err := st.GetMessage(ctx, a.ID); !errors.Is(err, convsvc.ErrNotFound) {
				t.Fatalf("messages survive the conversation: %v", err)
			}
		})
	}
}

func TestListPaging(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s := newService(st)
			var ids []string
			for i := 0; i < 5; i++ {
				c, err := s.Create(ctx, alice, fmt.Sprintf("c%d", i))
				if err != nil {
					t.Fatal(err)
				}
				ids = append([]string{c.ID}, ids...) // newest first
			}
			if _, err := s.Create(ctx, bob, "bobs"); err != nil {
				t.Fatal(err)
			}
			// a message moves its conversation to the top
			if _, err := s.Append(ctx, alice, ids[4], "", convsvc.Content{Text: "bump"}); err != nil {
				t.Fatal(err)
			}
			want := append([]string{ids[4]}, ids[:4]...)
			var got []string
			token := ""
			for pages := 0; ; pages++ {
				cs, next, err := s.List(ctx, alice, 2, token)
				if err != nil {
					t.Fatal(err)
				}
				for _, c := range cs {
					got = append(got, c.ID)
				}
				if next == "" {
					if pages != 2 {
						t.Fatalf("%d pages, want 3", pages+1)
					}
					break
				}
				token = next
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("listing = %v, want %v", got, want)
			}
			if _, _, err := s.List(ctx, alice, 2, "garbage"); !errors.Is(err, convsvc.ErrInvalid) {
				t.Fatalf("bad token: %v", err)
			}
		})
	}
}

func TestLimits(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s := newService(st)
			c, err := s.Create(ctx, alice, "t")
			if err != nil {
				t.Fatal(err)
			}
			bad := map[string]func() error{
				"long title": func() error { _, err := s.Create(ctx, alice, strings.Repeat("x", convsvc.MaxTitleBytes+1)); return err },
				"long text": func() error {
					_, err := s.Append(ctx, alice, c.ID, "", convsvc.Content{Text: strings.Repeat("x", convsvc.MaxTextBytes+1)})
					return err
				},
				"empty user text": func() error { _, err := s.Append(ctx, alice, c.ID, "", convsvc.Content{Text: " "}); return err },
				"bad role":        func() error { _, err := s.Append(ctx, alice, c.ID, "robot", convsvc.Content{Text: "x"}); return err },
				"bad status": func() error {
					_, err := s.Append(ctx, system, c.ID, convsvc.RoleAssistant, convsvc.Content{Status: "maybe"})
					return err
				},
				"action without type": func() error {
					_, err := s.Append(ctx, system, c.ID, convsvc.RoleAssistant, convsvc.Content{Actions: []convsvc.Action{{"args": 1}}})
					return err
				},
				"large actions": func() error {
					_, err := s.Append(ctx, system, c.ID, convsvc.RoleAssistant, convsvc.Content{Actions: []convsvc.Action{{"type": "x", "args": strings.Repeat("x", convsvc.MaxActionsBytes)}}})
					return err
				},
				"user message with actions": func() error {
					_, err := s.Append(ctx, alice, c.ID, "", convsvc.Content{Text: "x", Actions: []convsvc.Action{{"type": "x"}}})
					return err
				},
			}
			for what, f := range bad {
				if err := f(); !errors.Is(err, convsvc.ErrInvalid) {
					t.Errorf("%s: %v", what, err)
				}
			}
			if _, ms, _ := s.Get(ctx, alice, c.ID); len(ms) != 0 {
				t.Fatalf("refused messages were kept: %v", ms)
			}

			// messages per conversation: the limit is on the store, exercised with a small one
			for i := 1; i <= 3; i++ {
				if _, err := st.Append(ctx, convsvc.Message{ID: fmt.Sprintf("M%d", i), ConversationID: c.ID, Role: convsvc.RoleUser, Status: convsvc.StatusDone, CreatedAt: time.Now()}, 3); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.Append(ctx, convsvc.Message{ID: "M4", ConversationID: c.ID, Role: convsvc.RoleUser, Status: convsvc.StatusDone, CreatedAt: time.Now()}, 3); !errors.Is(err, convsvc.ErrFull) {
				t.Fatalf("fourth message: %v", err)
			}
			if _, err := st.Append(ctx, convsvc.Message{ID: "M5", ConversationID: "nope", Role: convsvc.RoleUser, CreatedAt: time.Now()}, 3); !errors.Is(err, convsvc.ErrNotFound) {
				t.Fatalf("message of nothing: %v", err)
			}
			// conversations per subject
			for i := 0; i < 2; i++ {
				if err := st.Create(ctx, convsvc.Conversation{ID: fmt.Sprintf("X%d", i), Subject: "carol", CreatedAt: time.Now(), UpdatedAt: time.Now()}, 2); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Create(ctx, convsvc.Conversation{ID: "X2", Subject: "carol", CreatedAt: time.Now(), UpdatedAt: time.Now()}, 2); !errors.Is(err, convsvc.ErrFull) {
				t.Fatalf("third conversation: %v", err)
			}
		})
	}
}

// Over the wire: the caller is the subject of the request, the platform acts under its system subject.
func TestHandler(t *testing.T) {
	ctx := context.Background()
	h := &convsvc.Handler{Service: newService(convsvc.NewMemoryStore())}
	path, handler := conversationsv1connect.NewConversationServiceHandler(h)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := conversationsv1connect.NewConversationServiceClient(srv.Client(), srv.URL)
	as := func(p authz.Principal, r connect.AnyRequest) { identity.SetHeaders(p, r.Header()) }

	cr := connect.NewRequest(&conversationsv1.CreateConversationRequest{Title: "First"})
	as(alice, cr)
	created, err := cl.CreateConversation(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Conversation.Id
	ap := connect.NewRequest(&conversationsv1.AppendMessageRequest{ConversationId: id, Text: "hello"})
	as(alice, ap)
	if _, err := cl.AppendMessage(ctx, ap); err != nil {
		t.Fatal(err)
	}
	act, _ := structpb.NewStruct(map[string]any{"type": "select_project", "args": map[string]any{"id": "PROJ-1"}})
	ap = connect.NewRequest(&conversationsv1.AppendMessageRequest{ConversationId: id, Role: "assistant", Status: "pending", ProcessId: "P1"})
	as(system, ap)
	am, err := cl.AppendMessage(ctx, ap)
	if err != nil {
		t.Fatal(err)
	}
	up := connect.NewRequest(&conversationsv1.UpdateMessageRequest{Id: am.Msg.Message.Id, Text: "Done.", Actions: []*structpb.Struct{act}, Status: "done"})
	as(system, up)
	if _, err := cl.UpdateMessage(ctx, up); err != nil {
		t.Fatal(err)
	}
	gr := connect.NewRequest(&conversationsv1.GetConversationRequest{Id: id})
	as(alice, gr)
	got, err := cl.GetConversation(ctx, gr)
	if err != nil || len(got.Msg.Messages) != 2 || got.Msg.Messages[1].Actions[0].AsMap()["type"] != "select_project" || got.Msg.Messages[1].Seq != 2 {
		t.Fatalf("get = %v, %v", got, err)
	}
	gr = connect.NewRequest(&conversationsv1.GetConversationRequest{Id: id})
	as(bob, gr)
	if _, err := cl.GetConversation(ctx, gr); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("bob: %v", err)
	}
	gr = connect.NewRequest(&conversationsv1.GetConversationRequest{Id: id})
	if _, err := cl.GetConversation(ctx, gr); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous: %v", err)
	}
	ap = connect.NewRequest(&conversationsv1.AppendMessageRequest{ConversationId: id, Role: "assistant", Text: "forged"})
	as(alice, ap)
	if _, err := cl.AppendMessage(ctx, ap); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("forged: %v", err)
	}
	ap = connect.NewRequest(&conversationsv1.AppendMessageRequest{ConversationId: id, Text: strings.Repeat("x", convsvc.MaxTextBytes+1)})
	as(alice, ap)
	if _, err := cl.AppendMessage(ctx, ap); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("large: %v", err)
	}
	lr := connect.NewRequest(&conversationsv1.ListConversationsRequest{})
	as(alice, lr)
	if l, err := cl.ListConversations(ctx, lr); err != nil || len(l.Msg.Conversations) != 1 || l.Msg.Conversations[0].Title != "First" {
		t.Fatalf("list = %v, %v", l, err)
	}
}
