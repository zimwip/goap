package graphsvc

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/identity"
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
// The requests carry the principal of their context: the engine acts for the initiator of a process, which the graph
// records on the events of the change impacts (ADR 0029).
func NewClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: graphv1connect.NewGraphServiceClient(hc, baseURL, append([]connect.ClientOption{identity.Forward()}, opts...)...)}
}

func (c *Client) CreateChange(ctx context.Context, in graph.NewChange) (domain.Change, error) {
	r, err := c.rpc.CreateChange(ctx, connect.NewRequest(&graphv1.CreateChangeRequest{Title: in.Title, Intent: in.Intent,
		Methodology: in.Methodology, Namespace: in.Namespace, BaselineId: string(in.BaselineID), Branch: in.Branch, OwnBranch: in.OwnBranch, ParentId: string(in.ParentID), OwnerOrg: in.OwnerOrg, Data: pbconv.Struct(in.Data)}))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
}

func (c *Client) UpdateChange(ctx context.Context, id domain.ChangeID, p graph.ChangePatch) (domain.Change, error) {
	req := &graphv1.UpdateChangeRequest{Id: string(id), Title: p.Title, Intent: p.Intent, Goal: p.Goal, Data: pbconv.Struct(p.Data)}
	if p.Status != nil {
		s := string(*p.Status)
		req.Status = &s
	}
	r, err := c.rpc.UpdateChange(ctx, connect.NewRequest(req))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
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

// AddNodes implements engine.GraphPort.
func (c *Client) AddNodes(ctx context.Context, id domain.ChangeID, nodes []domain.ChangeImpact) ([]domain.ChangeImpact, error) {
	r, err := c.rpc.AddChangeImpacts(ctx, connect.NewRequest(&graphv1.AddChangeImpactsRequest{ChangeId: string(id), Nodes: pbconv.ChangeImpactsToPB(nodes)}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactsFromPB(r.Msg.Nodes), nil
}

// WriteNode implements engine.GraphPort.
func (c *Client) WriteNode(ctx context.Context, id domain.ChangeID, node domain.ChangeImpactID, w graph.NodeWrite) (domain.ChangeImpact, error) {
	req := &graphv1.WriteChangeImpactRequest{ChangeId: string(id), ChangeImpactId: string(node), Props: pbconv.Struct(w.Properties), State: w.State, Retire: w.Retire, Flow: w.Flow, Execution: w.Execution}
	for _, l := range w.AddLinks {
		req.AddLinks = append(req.AddLinks, &graphv1.NodeLinkWrite{Type: l.Type, To: pbconv.RefToPB(l.To), Props: pbconv.Struct(l.Properties)})
	}
	for _, l := range w.RemoveLinks {
		req.RemoveLinks = append(req.RemoveLinks, string(l))
	}
	r, err := c.rpc.WriteChangeImpact(ctx, connect.NewRequest(req))
	if err != nil {
		return domain.ChangeImpact{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactFromPB(r.Msg.Node), nil
}

// ReviewNodeOn implements engine.GraphPort (the reviewer is the principal of the request).
func (c *Client) ReviewNodeOn(ctx context.Context, id domain.ChangeID, flow, execution string, node domain.ChangeImpactID, status domain.NodeReview, _, comment string) (domain.ChangeImpact, error) {
	r, err := c.rpc.ReviewChangeImpact(ctx, connect.NewRequest(&graphv1.ReviewChangeImpactRequest{ChangeId: string(id), ChangeImpactId: string(node),
		Accept: status == domain.ReviewAccepted, Comment: comment, Flow: flow, Execution: execution}))
	if err != nil {
		return domain.ChangeImpact{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactFromPB(r.Msg.Node), nil
}

// Changes lists every change known to the graph service.
func (c *Client) Changes(ctx context.Context) ([]domain.Change, error) {
	r, err := c.rpc.ListChanges(ctx, connect.NewRequest(&graphv1.ListChangesRequest{}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	out := make([]domain.Change, len(r.Msg.Changes))
	for i, ch := range r.Msg.Changes {
		out[i] = pbconv.ChangeFromPB(ch)
	}
	return out, nil
}

// ListChanges implements engine.GraphPort.
func (c *Client) ListChanges(ctx context.Context, f graph.ChangesFilter) ([]domain.Change, error) {
	req := &graphv1.ListChangesRequest{Namespace: f.Namespace, OwnerOrg: f.OwnerOrg}
	for _, s := range f.Status {
		req.Status = append(req.Status, string(s))
	}
	r, err := c.rpc.ListChanges(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	out := make([]domain.Change, len(r.Msg.Changes))
	for i, ch := range r.Msg.Changes {
		out[i] = pbconv.ChangeFromPB(ch)
	}
	return out, nil
}

// Commit runs a change of node edits in the graph service (see graph.Commit).
func (c *Client) Commit(ctx context.Context, in graph.Commit) (graph.CommitResult, error) {
	r, err := c.rpc.CommitEdits(ctx, connect.NewRequest(&graphv1.CommitEditsRequest{Namespace: in.Namespace, Title: in.Title, Intent: in.Intent,
		Methodology: in.Methodology, Data: pbconv.Struct(in.Data), BaselineId: string(in.Baseline), BaselineName: in.BaselineName, Edits: pbconv.EditsToPB(in.Edits)}))
	if err != nil {
		return graph.CommitResult{}, rpcerr.FromConnect(err)
	}
	return graph.CommitResult{Change: domain.ChangeID(r.Msg.ChangeId), Baseline: pbconv.BaselineFromPB(r.Msg.Baseline)}, nil
}

func (c *Client) Blackboard(ctx context.Context, id domain.ChangeID) (domain.Blackboard, error) {
	return c.BlackboardIn(ctx, id, "")
}

// BlackboardIn is the blackboard seen from a flow branch.
func (c *Client) BlackboardIn(ctx context.Context, id domain.ChangeID, flow string) (domain.Blackboard, error) {
	r, err := c.rpc.GetBlackboard(ctx, connect.NewRequest(&graphv1.GetBlackboardRequest{ChangeId: string(id), Flow: flow}))
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

func (c *Client) Baselines(ctx context.Context, namespace string) ([]domain.Baseline, error) {
	r, err := c.rpc.ListBaselines(ctx, connect.NewRequest(&graphv1.ListBaselinesRequest{Namespace: namespace}))
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

func (c *Client) BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error) {
	r, err := c.rpc.GetBranch(ctx, connect.NewRequest(&graphv1.GetBranchRequest{Namespace: namespace, Name: name}))
	if err != nil {
		return domain.Baseline{}, rpcerr.FromConnect(err)
	}
	return pbconv.BaselineFromPB(r.Msg.Head), nil
}

func (c *Client) CreateBaseline(ctx context.Context, namespace, name string, nodes []domain.NodeRef) (domain.Baseline, error) {
	req := &graphv1.CreateBaselineRequest{Namespace: namespace, Name: name}
	for _, n := range nodes {
		req.Nodes = append(req.Nodes, pbconv.RefToPB(n))
	}
	r, err := c.rpc.CreateBaseline(ctx, connect.NewRequest(req))
	if err != nil {
		return domain.Baseline{}, rpcerr.FromConnect(err)
	}
	return pbconv.BaselineFromPB(r.Msg.Baseline), nil
}

// OpenFlow implements engine.GraphPort.
func (c *Client) OpenFlow(ctx context.Context, id domain.ChangeID, in graph.OpenFlowRequest) (domain.Flow, error) {
	r, err := c.rpc.OpenFlow(ctx, connect.NewRequest(&graphv1.OpenFlowRequest{ChangeId: string(id), Parent: in.Parent, ForkAfter: string(in.ForkAfter),
		Seeds: seedsToPB(in.Seeds), FromStep: int32(in.FromStep), Execution: in.Execution, Process: in.Process, Reason: in.Reason,
		Guidance: in.Guidance, By: in.By, StaleExecutions: in.StaleExecutions}))
	if err != nil {
		return domain.Flow{}, rpcerr.FromConnect(err)
	}
	return pbconv.FlowFromPB(r.Msg.Flow), nil
}

func seedsToPB(ids []domain.ItemID) []string {
	var out []string
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

// AdoptFlow implements engine.GraphPort (the adopting principal comes from the request identity).
func (c *Client) AdoptFlow(ctx context.Context, id domain.ChangeID, flow, _ string) (domain.Flow, error) {
	r, err := c.rpc.AdoptFlow(ctx, connect.NewRequest(&graphv1.AdoptFlowRequest{ChangeId: string(id), Flow: flow}))
	if err != nil {
		return domain.Flow{}, rpcerr.FromConnect(err)
	}
	return pbconv.FlowFromPB(r.Msg.Flow), nil
}

// DiscardFlow implements engine.GraphPort.
func (c *Client) DiscardFlow(ctx context.Context, id domain.ChangeID, flow, _ string) (domain.Flow, error) {
	r, err := c.rpc.DiscardFlow(ctx, connect.NewRequest(&graphv1.DiscardFlowRequest{ChangeId: string(id), Flow: flow}))
	if err != nil {
		return domain.Flow{}, rpcerr.FromConnect(err)
	}
	return pbconv.FlowFromPB(r.Msg.Flow), nil
}

// ValidateBoard implements engine.GraphPort.
func (c *Client) ValidateBoard(ctx context.Context, id domain.ChangeID, flow string) ([]domain.BoardIssue, error) {
	r, err := c.rpc.ValidateBoard(ctx, connect.NewRequest(&graphv1.ValidateBoardRequest{ChangeId: string(id), Flow: flow}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	var out []domain.BoardIssue
	for _, i := range r.Msg.Issues {
		out = append(out, pbconv.BoardIssueFromPB(i))
	}
	return out, nil
}
