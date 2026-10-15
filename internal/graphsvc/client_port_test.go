package graphsvc

import "github.com/zimwip/goap/pkg/engine"

// The client is the engine's graph port over RPC; the check lives in a test so the service does not import the engine.
var _ engine.GraphPort = (*Client)(nil)
