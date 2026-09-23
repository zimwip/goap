package graphsvc

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
)

// Client adapts the graph Connect client to engine.GraphPort.
type Client struct {
	rpc graphv1connect.GraphServiceClient
}

var _ engine.GraphPort = (*Client)(nil)

// NewClient returns a client for the graph service at baseURL.
func NewClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: graphv1connect.NewGraphServiceClient(hc, baseURL, opts...)}
}

func (c *Client) CreateChange(ctx context.Context, in graph.NewChange) (domain.ChangeSet, error) {
	r, err := c.rpc.CreateChange(ctx, connect.NewRequest(&graphv1.CreateChangeRequest{Title: in.Title, Intent: in.Intent,
		Methodology: in.Methodology, BaselineId: string(in.BaselineID), Branch: in.Branch, Data: pbconv.Struct(in.Data)}))
	if err != nil {
		return domain.ChangeSet{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
}

func (c *Client) UpdateChange(ctx context.Context, id domain.ChangeID, p graph.ChangePatch) (domain.ChangeSet, error) {
	req := &graphv1.UpdateChangeRequest{Id: string(id), Goal: p.Goal, Data: pbconv.Struct(p.Data)}
	if p.Status != nil {
		s := string(*p.Status)
		req.Status = &s
	}
	r, err := c.rpc.UpdateChange(ctx, connect.NewRequest(req))
	if err != nil {
		return domain.ChangeSet{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
}

func (c *Client) AddItems(ctx context.Context, id domain.ChangeID, items []domain.ChangeItem) ([]domain.ChangeItem, error) {
	r, err := c.rpc.AddItems(ctx, connect.NewRequest(&graphv1.AddItemsRequest{ChangeId: string(id), Items: pbconv.ItemsToPB(items)}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	return pbconv.ItemsFromPB(r.Msg.Items), nil
}

func (c *Client) Blackboard(ctx context.Context, id domain.ChangeID) (domain.Blackboard, error) {
	r, err := c.rpc.GetBlackboard(ctx, connect.NewRequest(&graphv1.GetBlackboardRequest{ChangeId: string(id)}))
	if err != nil {
		return domain.Blackboard{}, rpcerr.FromConnect(err)
	}
	bb := domain.Blackboard{Change: pbconv.ChangeFromPB(r.Msg.Change), Nodes: map[domain.NodeRef]domain.NodeView{}, Neighbors: map[domain.NodeRef]domain.Node{}}
	for _, v := range r.Msg.Nodes {
		nv := pbconv.ViewFromPB(v)
		bb.Nodes[nv.Ref()] = nv
	}
	for _, n := range r.Msg.Neighbors {
		nn := pbconv.NodeFromPB(n)
		bb.Neighbors[nn.Ref()] = nn
	}
	return bb, nil
}

func (c *Client) BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error) {
	r, err := c.rpc.GetBaselineGraph(ctx, connect.NewRequest(&graphv1.GetBaselineGraphRequest{Id: string(id)}))
	if err != nil {
		return nil, nil, rpcerr.FromConnect(err)
	}
	return pbconv.NodesFromPB(r.Msg.Nodes), pbconv.LinksFromPB(r.Msg.Links), nil
}

func (c *Client) Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	r, err := c.rpc.ApplyChange(ctx, connect.NewRequest(&graphv1.ApplyChangeRequest{ChangeId: string(id), BaselineName: baselineName}))
	if err != nil {
		return domain.Baseline{}, rpcerr.FromConnect(err)
	}
	return pbconv.BaselineFromPB(r.Msg.Baseline), nil
}

func (c *Client) Baselines(ctx context.Context) ([]domain.Baseline, error) {
	r, err := c.rpc.ListBaselines(ctx, connect.NewRequest(&graphv1.ListBaselinesRequest{}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	out := make([]domain.Baseline, len(r.Msg.Baselines))
	for i, b := range r.Msg.Baselines {
		out[i] = pbconv.BaselineFromPB(b)
	}
	return out, nil
}

func (c *Client) Record(ctx context.Context, recs []domain.ExecutionRecord) error {
	_, err := c.rpc.RecordExecutions(ctx, connect.NewRequest(&graphv1.RecordExecutionsRequest{Records: pbconv.ExecutionsToPB(recs)}))
	return rpcerr.FromConnect(err)
}

func (c *Client) Journal(ctx context.Context, f domain.ExecutionFilter) ([]domain.ExecutionRecord, error) {
	r, err := c.rpc.ListExecutions(ctx, connect.NewRequest(&graphv1.ListExecutionsRequest{ChangeId: string(f.ChangeID), ProcessIds: f.ProcessIDs}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	return pbconv.ExecutionsFromPB(r.Msg.Records), nil
}

func (c *Client) BranchHead(ctx context.Context, name string) (domain.Baseline, error) {
	r, err := c.rpc.GetBranch(ctx, connect.NewRequest(&graphv1.GetBranchRequest{Name: name}))
	if err != nil {
		return domain.Baseline{}, rpcerr.FromConnect(err)
	}
	return pbconv.BaselineFromPB(r.Msg.Head), nil
}

func (c *Client) CreateBaseline(ctx context.Context, name string, nodes []domain.NodeRef) (domain.Baseline, error) {
	req := &graphv1.CreateBaselineRequest{Name: name}
	for _, n := range nodes {
		req.Nodes = append(req.Nodes, pbconv.RefToPB(n))
	}
	r, err := c.rpc.CreateBaseline(ctx, connect.NewRequest(req))
	if err != nil {
		return domain.Baseline{}, rpcerr.FromConnect(err)
	}
	return pbconv.BaselineFromPB(r.Msg.Baseline), nil
}
