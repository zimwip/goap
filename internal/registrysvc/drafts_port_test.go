package registrysvc

import (
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/selfimprove"
)

// The registry implements the ports of the engine and of the self-observation without importing them (ADR 0098 §10).
var (
	_ selfimprove.MethodologyDrafts = Drafts{}
	_ selfimprove.MethodologyDrafts = (*Client)(nil)
	_ engine.MethodologyPort        = (*Client)(nil)
	_ engine.Publisher              = Publisher(nil)
)
