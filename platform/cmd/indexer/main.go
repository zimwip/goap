// Command indexer is the document index service (ADR 0026, 0095): it follows the node, baseline and change events of the graph
// on NATS, embeds through the model gateway and answers hybrid searches.
package main

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/contracts/gen/goap/index/v1/indexv1connect"
	"github.com/zimwip/goap/graph/index"
	"github.com/zimwip/goap/graph/indexersvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/platform/modelgw"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("indexer")
	defer telemetry.Setup(context.Background(), log, "indexer")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store index.Store = index.NewMemory()
	if db := platform.OptionalSQL(ctx, log, "index", index.Migrations, index.SQLiteMigrations); db != nil {
		defer db.Close()
		if db.SQLite() {
			store = index.NewSQLite(db.DB)
		} else {
			store = index.NewPostgres(db.Pool)
		}
		srv.Readiness(db.Ping)
	}
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()
	srv.Readiness(events.Ready)

	hc := platform.H2CClient()
	graphURL := platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")
	directory := &access.Directory{Graph: graphclient.NewClient(hc, graphURL, telemetry.ClientOptions()...)}
	// the policies, the organisation and the projects the platform graph delivers, read again when its baseline
	// events say the organisation moved (ADR 0154 §9)
	if events != nil {
		if err := directory.FollowEvents(events); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	iam, err := access.NewAuthorizer(directory)
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	// embeddings: the model gateway, alias "embed"; without one the index is text only
	var embedder = modelgw.NewClient(hc, platform.Env("GOAP_MODELGW_URL", "http://localhost:8084"), telemetry.ClientOptions()...)
	svc := indexersvc.New(store, embedder, iam, log)
	svc.Access = directory                                                        // the projects a caller may see, and a project with its sub-projects (ADR 0095)
	svc.Views = graphclient.NewClient(hc, graphURL, telemetry.ClientOptions()...) // what the views a search names hold (ADR 0157)
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
