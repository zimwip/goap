package enginesvc

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	enginev1 "github.com/zimwip/goap/gen/goap/engine/v1"
	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/changeapi"
	"github.com/zimwip/goap/pkg/domain"
)

// GuardianClient is the guardian of the changes governed by a methodology as the graph asks it over the network
// (ADR 0098): the engine's GuardianService, called with the identity of the graph's caller.
type GuardianClient struct {
	rpc enginev1connect.GuardianServiceClient
}

var _ changeapi.Guardian = (*GuardianClient)(nil)

// NewGuardianClient returns a client of the guardian served by the engine at baseURL.
func NewGuardianClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *GuardianClient {
	opts = append([]connect.ClientOption{identity.Forward()}, opts...)
	return &GuardianClient{rpc: enginev1connect.NewGuardianServiceClient(hc, baseURL, opts...)}
}

// MayCommit implements changeapi.Guardian.
func (c *GuardianClient) MayCommit(ctx context.Context, ch domain.Change, bb domain.Blackboard) (bool, bool, error) {
	bb.Change = ch
	r, err := c.rpc.MayCommit(ctx, connect.NewRequest(&enginev1.MayCommitRequest{Blackboard: pbconv.BlackboardToPB(bb)}))
	if err != nil {
		return false, false, rpcerr.FromConnect(err)
	}
	return r.Msg.Decided, r.Msg.Ok, nil
}

// MayCreateChild implements changeapi.Guardian.
func (c *GuardianClient) MayCreateChild(ctx context.Context, parent, child domain.Change) error {
	_, err := c.rpc.MayCreateChild(ctx, connect.NewRequest(&enginev1.MayCreateChildRequest{Parent: pbconv.ChangeToPB(parent), Child: pbconv.ChangeToPB(child)}))
	return rpcerr.FromConnect(err)
}

// MayMove implements changeapi.Guardian.
func (c *GuardianClient) MayMove(ctx context.Context, family []domain.Change, to string) error {
	pb := make([]*graphv1.Change, 0, len(family))
	for _, ch := range family {
		pb = append(pb, pbconv.ChangeToPB(ch))
	}
	_, err := c.rpc.MayMove(ctx, connect.NewRequest(&enginev1.MayMoveRequest{Family: pb, To: to}))
	return rpcerr.FromConnect(err)
}

// MayEdit implements changeapi.Guardian.
func (c *GuardianClient) MayEdit(ctx context.Context, ch domain.Change, impact domain.ChangeImpactID) error {
	_, err := c.rpc.MayEdit(ctx, connect.NewRequest(&enginev1.MayEditRequest{Change: pbconv.ChangeToPB(ch), ImpactId: string(impact)}))
	return rpcerr.FromConnect(err)
}
