// Command indexer is the node index service (ADR 0026): it follows the node and baseline events of the graph
// on NATS, embeds through the model gateway and answers hybrid searches.
package main

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/gen/goap/index/v1/indexv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/indexersvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/index"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("indexer")
	defer telemetry.Setup(context.Background(), log, "indexer")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store index.Store = index.NewMemory()
	if pool := platform.OptionalPostgres(ctx, log, index.Migrations); pool != nil {
		defer pool.Close()
		store = index.NewPostgres(pool)
		srv.Readiness(pool.Ping)
	}
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()
	srv.Readiness(events.Ready)

	hc := platform.H2CClient()
	graphURL := platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")
	iam, err := access.NewAuthorizer(&access.Directory{Graph: graphsvc.NewClient(hc, graphURL, telemetry.ClientOptions()...)})
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	// embeddings: the model gateway, alias "embed"; without one the index is text only
	var embedder = modelgw.NewClient(hc, platform.Env("GOAP_MODELGW_URL", "http://localhost:8084"), telemetry.ClientOptions()...)
	svc := indexersvc.New(store, embedder, iam, log)
	graphRPC := graphv1connect.NewGraphServiceClient(hc, graphURL, telemetry.ClientOptions()...)
	svc.Republish = func(ctx context.Context) (int, error) {
		req := connect.NewRequest(&graphv1.RepublishIndexRequest{})
		p := authz.From(ctx)
		req.Header().Set(identity.HeaderSubject, p.Subject)
		req.Header().Set(identity.HeaderOrg, p.Org)
		req.Header().Set(identity.HeaderRoles, strings.Join(p.Roles, ","))
		r, err := graphRPC.RepublishIndex(ctx, req)
		if err != nil {
			return 0, err
		}
		return int(r.Msg.Versions), nil
	}
	if events != nil {
		go func() {
			if err := events.ConsumeDurable(ctx, log, "indexer", indexersvc.Subjects, svc.Handle); err != nil {
				platform.Fatal(log, "index consumer", err)
			}
		}()
	} else {
		log.Warn("no event bus: the index only changes through Reindex")
	}
	srv.Mount(indexv1connect.NewIndexServiceHandler(&indexersvc.Handler{Service: svc, Authz: iam}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
