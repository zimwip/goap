package graphsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/journal"

	"connectrpc.com/connect"
)

// The execution journal through the service: the engine's client appends the entries of its records to the log of the
// change (journal.Append over graphsvc.Client) and reads them back as entries, the web reads them as records.
func TestJournalThroughTheService(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	path, handler := graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := graphsvc.NewClient(srv.Client(), srv.URL)

	base, err := g.BranchHead(ctx, "", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "t", BaselineID: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	rec := journal.Record{ChangeID: c.ID, ProcessID: "p1", Kind: journal.KindAction, Action: "a", Step: 2,
		ModelCalls: []journal.ModelCall{{Model: "fast", InputTokens: 3, Exchange: &journal.ModelExchange{Response: "ok"}}}}
	if err := journal.Append(ctx, cl, []journal.Record{rec}); err != nil {
		t.Fatal(err)
	}
	got, err := journal.Read(ctx, cl, journal.Filter{ProcessIDs: []string{"p1"}})
	if err != nil || len(got) != 1 || got[0].Action != "a" || got[0].ModelCalls[0].InputTokens != 3 || got[0].ModelCalls[0].Exchange != nil {
		t.Fatalf("records: %+v %v", got, err)
	}
	// the prompts are entries of their own, read through the log with the change named
	entries, _, err := cl.ChangeLog(ctx, domain.LogFilter{Change: c.ID, Types: []string{journal.StreamModel + "."}})
	if err != nil || len(entries) != 1 {
		t.Fatalf("model entries: %v %v", entries, err)
	}
	// the streams of the graph are not written through the service
	if err := cl.AppendLog(ctx, []domain.LogEntry{{Change: c.ID, Type: domain.LogFact + ".artifact"}}); err == nil {
		t.Fatal("a fact was appended through the log")
	}
	// the web reads records
	rpc := graphv1connect.NewGraphServiceClient(srv.Client(), srv.URL)
	r, err := rpc.ListExecutions(ctx, connect.NewRequest(&graphv1.ListExecutionsRequest{ChangeId: string(c.ID)}))
	if err != nil || len(r.Msg.Records) != 1 || r.Msg.Records[0].Action != "a" {
		t.Fatalf("ListExecutions: %+v %v", r, err)
	}
	if _, err := rpc.ListExecutions(ctx, connect.NewRequest(&graphv1.ListExecutionsRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("an empty filter: %v", err)
	}
}
