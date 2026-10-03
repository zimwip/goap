package platform

import "context"

// Stamp says who issued the request being served and the client-generated id of its command (ADR 0053). It rides
// the context of a service, and the bus carries it as message headers, so the events a write causes name both.
type Stamp struct {
	Actor, Command string
}

type stampKey struct{}

// Message headers carrying a Stamp on the bus.
const (
	HeaderActor   = "X-Goap-Actor"
	HeaderCommand = "X-Goap-Command"
)

// WithStamp returns ctx carrying the stamp.
func WithStamp(ctx context.Context, s Stamp) context.Context {
	return context.WithValue(ctx, stampKey{}, s)
}

// StampOf returns the stamp of ctx (zero when none).
func StampOf(ctx context.Context) Stamp {
	s, _ := ctx.Value(stampKey{}).(Stamp)
	return s
}
