package connectorkit_test

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/gen/goap/connector/v1/connectorv1connect"
)

// connectorv1connectClient returns a call to Invoke without the connector token.
func connectorv1connectClient(url string) func(context.Context) (*connect.Response[connectorv1.InvokeResponse], error) {
	c := connectorv1connect.NewConnectorServiceClient(http.DefaultClient, url)
	return func(ctx context.Context) (*connect.Response[connectorv1.InvokeResponse], error) {
		return c.Invoke(ctx, connect.NewRequest(&connectorv1.InvokeRequest{Operation: "list_dir"}))
	}
}
