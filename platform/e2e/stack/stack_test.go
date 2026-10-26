// Package stacke2e tests a running platform through its gateway, as a person uses it: the local stack of goap-local
// (make local-test starts one on a fresh directory), or the compose stack. It runs only when GOAP_STACK_URL names the
// gateway, and writes only on a fresh stack, where the person it registers is the first and so an administrator.
package stacke2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	enginev1 "github.com/zimwip/goap/contracts/gen/goap/engine/v1"
	"github.com/zimwip/goap/contracts/gen/goap/engine/v1/enginev1connect"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	indexv1 "github.com/zimwip/goap/contracts/gen/goap/index/v1"
	"github.com/zimwip/goap/contracts/gen/goap/index/v1/indexv1connect"
	jobsv1 "github.com/zimwip/goap/contracts/gen/goap/jobs/v1"
	"github.com/zimwip/goap/contracts/gen/goap/jobs/v1/jobsv1connect"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
)

// session is a person signed in at the gateway.
type session struct {
	gateway string
	token   string
	http    *http.Client
}

// signIn registers a person (local sign-in) and answers its session.
func signIn(t *testing.T, gateway, subject string) *session {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"subject": subject, "password": "a long enough password"})
	resp, err := http.Post(gateway+"/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	var out struct{ Token string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK || out.Token == "" {
		t.Fatalf("register: %s %v", resp.Status, err)
	}
	return &session{gateway: gateway, token: out.Token, http: &http.Client{Timeout: time.Minute}}
}

// options are the client options of the session's calls: its bearer token.
func (s *session) options() connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+s.token)
			return next(ctx, req)
		}
	}))
}

// whoami answers the session of the person.
func (s *session) whoami(t *testing.T) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, s.gateway+"/api/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := s.http.Do(req)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %s %v", resp.Status, err)
	}
	return out
}

// eventually retries f until it holds or the time runs out.
func eventually(t *testing.T, what string, limit time.Duration, f func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(limit)
	var last error
	for time.Now().Before(deadline) {
		ok, err := f()
		if ok {
			return
		}
		last = err
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s: not after %s (last error: %v)", what, limit, last)
}

func TestStack(t *testing.T) {
	gateway := os.Getenv("GOAP_STACK_URL")
	if gateway == "" {
		t.Skip("GOAP_STACK_URL is not set: no running stack to test (make local-test)")
	}
	ctx := context.Background()
	run := fmt.Sprintf("e2e-%d", time.Now().UnixNano())
	s := signIn(t, gateway, run)
	who := s.whoami(t)
	if who["subject"] != run {
		t.Fatalf("whoami subject %v, want %s", who["subject"], run)
	}
	global, _ := who["global"].([]any)
	if !slices.Contains(global, any("admin")) {
		t.Skip("the person registered is not the first: run on a fresh stack (goap-local -reset) to test the writes")
	}

	t.Run("registry seeded the methodologies", func(t *testing.T) {
		reg := registryv1connect.NewRegistryServiceClient(s.http, gateway, s.options())
		eventually(t, "methodologies", time.Minute, func() (bool, error) {
			resp, err := reg.ListMethodologies(ctx, connect.NewRequest(&registryv1.ListMethodologiesRequest{}))
			if err != nil {
				return false, err
			}
			return slices.ContainsFunc(resp.Msg.Methodologies, func(m *registryv1.MethodologySummary) bool { return m.Name == "sdlc" }), nil
		})
	})

	graph := graphv1connect.NewGraphServiceClient(s.http, gateway, s.options())
	key := "NEED-" + run
	title := "Stack test need " + run
	t.Run("a commit on the content graph", func(t *testing.T) {
		props, _ := structpb.NewStruct(map[string]any{"title": title, "description": "Written by the stack test"})
		_, err := graph.CommitEdits(ctx, connect.NewRequest(&graphv1.CommitEditsRequest{
			Namespace: "alm", Title: "Stack test", Intent: "check the platform end to end", ProjectId: "PROJ-ROOT",
			Edits: []*graphv1.NodeEdit{{Key: key, Type: "alm@Need", Props: props}},
		}))
		if err != nil {
			t.Fatalf("commit: %v", err)
		}
		node, err := graph.GetNode(ctx, connect.NewRequest(&graphv1.GetNodeRequest{Key: key, View: &graphv1.View{Namespace: "alm", Branch: "main"}}))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if got := node.Msg.View.GetNode().GetProps().AsMap()["title"]; got != title {
			t.Fatalf("title %v, want %s", got, title)
		}
	})

	t.Run("the index follows the commit over NATS", func(t *testing.T) {
		index := indexv1connect.NewIndexServiceClient(s.http, gateway, s.options())
		main := true
		eventually(t, "search hit", time.Minute, func() (bool, error) {
			resp, err := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{Text: run, Namespaces: []string{"alm"}, History: true, Main: &main}))
			if err != nil {
				return false, err
			}
			return slices.ContainsFunc(resp.Msg.Hits, func(h *indexv1.Hit) bool { return h.Key == key }), nil
		})
	})

	t.Run("the methodology of a change is asked of the registry", func(t *testing.T) {
		// the graph knows no methodology (ADR 0168): what a change follows and the gate of its moves are the
		// registry's answers, on the blackboard the graph sends
		created, err := graph.CreateChange(ctx, connect.NewRequest(&graphv1.CreateChangeRequest{
			Title: "Stack test change " + run, Namespace: "alm", ProjectId: "PROJ-ROOT", Methodology: "sdlc", OwnBranch: true,
		}))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		c := created.Msg.Change
		if c.Lifecycle != "maturity_of_change" || c.State != "framing" || c.MethodologyVersion == "" || len(c.Guardians) == 0 {
			t.Fatalf("change follows %s@%s, lifecycle %q in %q, guardians %v", c.Methodology, c.MethodologyVersion, c.Lifecycle, c.State, c.Guardians)
		}
		// the framing is not confirmed: its gate refuses the analysis (a blackboard the registry could not read would be
		// an invalid argument, an unreachable registry unavailable)
		_, err = graph.TransitionChange(ctx, connect.NewRequest(&graphv1.TransitionChangeRequest{ChangeId: c.Id, Transition: "analyse"}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "cannot take analyse") {
			t.Fatalf("analyse before the framing is confirmed: %v, want a refusal of its gate", err)
		}
		moved, err := graph.TransitionChange(ctx, connect.NewRequest(&graphv1.TransitionChangeRequest{ChangeId: c.Id, Transition: "abandon"}))
		if err != nil {
			t.Fatalf("abandon: %v", err)
		}
		if moved.Msg.Change.State != "abandoned" {
			t.Fatalf("state %q, want abandoned", moved.Msg.Change.State)
		}
	})

	t.Run("an export job runs on the graph's worker", func(t *testing.T) {
		start, err := graph.StartGraphExport(ctx, connect.NewRequest(&graphv1.StartGraphExportRequest{Namespace: "alm", Format: "json"}))
		if err != nil {
			t.Fatalf("start export: %v", err)
		}
		jobs := jobsv1connect.NewJobServiceClient(s.http, gateway, s.options())
		eventually(t, "export done", 2*time.Minute, func() (bool, error) {
			resp, err := jobs.GetJob(ctx, connect.NewRequest(&jobsv1.GetJobRequest{Id: start.Msg.Job.Id}))
			if err != nil {
				return false, err
			}
			switch st := resp.Msg.Job.Status; st {
			case "done":
				return true, nil
			case "failed", "cancelled":
				t.Fatalf("export %s: %s", st, resp.Msg.Job.Progress)
			}
			return false, fmt.Errorf("status %s", resp.Msg.Job.Status)
		})
	})

	t.Run("the engine answers", func(t *testing.T) {
		eng := enginev1connect.NewEngineServiceClient(s.http, gateway, s.options())
		if _, err := eng.ListProcesses(ctx, connect.NewRequest(&enginev1.ListProcessesRequest{})); err != nil {
			t.Fatalf("list processes: %v", err)
		}
	})
}
