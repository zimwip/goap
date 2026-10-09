package graphsvc_test

import (
	"net/http"

	"github.com/zimwip/goap/gen/goap/change/v1/changev1connect"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
)

// rpcClient calls both services of the graph process: the graph and the change (ADR 0098 §9).
type rpcClient struct {
	graphv1connect.GraphServiceClient
	changev1connect.ChangeServiceClient
}

func newRPCClient(hc *http.Client, url string) rpcClient {
	return rpcClient{graphv1connect.NewGraphServiceClient(hc, url), changev1connect.NewChangeServiceClient(hc, url)}
}
