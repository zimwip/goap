package graphclient

import "github.com/zimwip/goap/contracts/graphapi"

// The client is the graph as its callers see it over RPC (graphapi.Port, the engine's GraphPort).
var _ graphapi.Port = (*Client)(nil)
