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
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
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
//
// A request whose context carries no principal (a directory refreshing its snapshot in the background) goes as the
// service client, a named principal with no role: the graph refuses anonymous requests (Handler.Identify).
func NewClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: graphv1connect.NewGraphServiceClient(hc, baseURL, append([]connect.ClientOption{identity.Forward(), serviceIdentity()}, opts...)...)}
}

// ClientPrincipal is the identity of a graph client acting for no one in particular.
var ClientPrincipal = authz.System("client")

func serviceIdentity() connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Spec().IsClient && req.Header().Get(identity.HeaderSubject) == "" {
				identity.SetHeaders(ClientPrincipal, req.Header())
			}
			return next(ctx, req)
		}
	}))
}

// DeclareUser makes sure the User node of a subject exists (ADR 0042: a user is declared the moment they sign
// in), for a caller with no graph in process (the gateway). It reads that node as the subject: the service's
// EnsureCaller interceptor creates it first when missing (EnsureUser, member of the unit new users join), so
// finding it proves it was created, and not finding it reports why it could not be.
func (c *Client) DeclareUser(ctx context.Context, subject string) error {
	ctx = authz.With(ctx, authz.Principal{Subject: subject})
	_, err := c.rpc.GetNode(ctx, connect.NewRequest(&graphv1.GetNodeRequest{Namespace: access.NamespaceOrganisation, Key: access.UserKey(subject)}))
	return rpcerr.FromConnect(err)
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

// ProposeImpact implements engine.GraphPort.
func (c *Client) ProposeImpact(ctx context.Context, id domain.ChangeID, nodes []domain.ChangeImpact) ([]domain.ChangeImpact, error) {
	r, err := c.rpc.ProposeImpact(ctx, connect.NewRequest(&graphv1.ProposeImpactRequest{ChangeId: string(id), Nodes: pbconv.ChangeImpactsToPB(nodes)}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactsFromPB(r.Msg.Nodes), nil
}

// ImpactNodeCreate creates a node in a change (ADR 0076).
func (c *Client) ImpactNodeCreate(ctx context.Context, id domain.ChangeID, in graph.NodeCreate) (domain.ChangeImpact, error) {
	req := &graphv1.ImpactNodeCreateRequest{ChangeId: string(id), Key: in.Key, Type: in.Type, Props: pbconv.Struct(in.Properties), Owner: in.Owner,
		Rationale: in.Rationale, Flow: in.Flow, Execution: in.Execution}
	for _, l := range in.Links {
		req.Links = append(req.Links, &graphv1.NodeLinkWrite{Type: l.Type, To: pbconv.RefToPB(l.To), Props: pbconv.Struct(l.Properties)})
	}
	r, err := c.rpc.ImpactNodeCreate(ctx, connect.NewRequest(req))
	if err != nil {
		return domain.ChangeImpact{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactFromPB(r.Msg.Node), nil
}

func nameToPB(n graph.NodeName) *graphv1.NodeName {
	return &graphv1.NodeName{ChangeImpactId: string(n.Impact), NodeId: string(n.Node), Key: n.Key}
}

func specToPB(c graph.NodeCreate) *graphv1.NodeCreateSpec {
	s := &graphv1.NodeCreateSpec{Key: c.Key, Type: c.Type, Props: pbconv.Struct(c.Properties), Owner: c.Owner, Rationale: c.Rationale}
	for _, l := range c.Links {
		s.Links = append(s.Links, &graphv1.NodeLinkWrite{Type: l.Type, To: pbconv.RefToPB(l.To), Props: pbconv.Struct(l.Properties)})
	}
	return s
}

// ImpactNodeMerge implements engine.GraphPort (ADR 0077). The Gate of the input is the handler's here.
func (c *Client) ImpactNodeMerge(ctx context.Context, id domain.ChangeID, in graph.MergeInput) (graph.Restructured, error) {
	req := &graphv1.ImpactNodeMergeRequest{ChangeId: string(id), Into: specToPB(in.Into), Rationale: in.Rationale, Flow: in.Flow, Execution: in.Execution}
	for _, s := range in.Sources {
		req.Sources = append(req.Sources, nameToPB(s))
	}
	r, err := c.rpc.ImpactNodeMerge(ctx, connect.NewRequest(req))
	if err != nil {
		return graph.Restructured{}, rpcerr.FromConnect(err)
	}
	return pbconv.RestructuredFromPB(r.Msg), nil
}

// ImpactNodeSplit implements engine.GraphPort (ADR 0077).
func (c *Client) ImpactNodeSplit(ctx context.Context, id domain.ChangeID, in graph.SplitInput) (graph.Restructured, error) {
	req := &graphv1.ImpactNodeSplitRequest{ChangeId: string(id), Source: nameToPB(in.Source), Rationale: in.Rationale, Flow: in.Flow, Execution: in.Execution}
	for _, s := range in.Into {
		req.Into = append(req.Into, specToPB(s))
	}
	r, err := c.rpc.ImpactNodeSplit(ctx, connect.NewRequest(req))
	if err != nil {
		return graph.Restructured{}, rpcerr.FromConnect(err)
	}
	return pbconv.RestructuredFromPB(r.Msg), nil
}

// DerivedNodes returns the nodes that derive from a node (ADR 0077).
func (c *Client) DerivedNodes(ctx context.Context, ref domain.NodeRef) ([]domain.Node, error) {
	r, err := c.rpc.DerivedNodes(ctx, connect.NewRequest(&graphv1.DerivedNodesRequest{Ref: pbconv.RefToPB(ref)}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	return pbconv.NodesFromPB(r.Msg.Nodes), nil
}

// ImpactNodeCheckout implements engine.GraphPort.
func (c *Client) ImpactNodeCheckout(ctx context.Context, id domain.ChangeID, in graph.NodeCheckout) (domain.ChangeImpact, error) {
	r, err := c.rpc.ImpactNodeCheckout(ctx, connect.NewRequest(&graphv1.ImpactNodeCheckoutRequest{ChangeId: string(id), ChangeImpactId: string(in.Impact), NodeId: string(in.Node),
		Rationale: in.Rationale, Flow: in.Flow, Execution: in.Execution}))
	if err != nil {
		return domain.ChangeImpact{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactFromPB(r.Msg.Node), nil
}

// ImpactNodeUpdate implements engine.GraphPort.
func (c *Client) ImpactNodeUpdate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, in graph.NodeUpdate) (domain.ChangeImpact, error) {
	r, err := c.rpc.ImpactNodeUpdate(ctx, connect.NewRequest(&graphv1.ImpactNodeUpdateRequest{ChangeId: string(id), ChangeImpactId: string(impact), Props: pbconv.Struct(in.Properties),
		Owner: in.Owner, Flow: in.Flow, Execution: in.Execution}))
	if err != nil {
		return domain.ChangeImpact{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactFromPB(r.Msg.Node), nil
}

// ImpactLinkCreate implements engine.GraphPort.
func (c *Client) ImpactLinkCreate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, l graph.LinkWrite, flow, execution string) (domain.Link, error) {
	r, err := c.rpc.ImpactLinkCreate(ctx, connect.NewRequest(&graphv1.ImpactLinkCreateRequest{ChangeId: string(id), ChangeImpactId: string(impact), Type: l.Type, To: pbconv.RefToPB(l.To),
		Props: pbconv.Struct(l.Properties), Flow: flow, Execution: execution}))
	if err != nil {
		return domain.Link{}, rpcerr.FromConnect(err)
	}
	return pbconv.LinkFromPB(r.Msg.Link), nil
}

// ImpactLinkUpdate replaces the properties of a link of a draft.
func (c *Client) ImpactLinkUpdate(ctx context.Context, id domain.ChangeID, link domain.LinkID, props map[string]any, flow, execution string) (domain.Link, error) {
	r, err := c.rpc.ImpactLinkUpdate(ctx, connect.NewRequest(&graphv1.ImpactLinkUpdateRequest{ChangeId: string(id), LinkId: string(link), Props: pbconv.Struct(props), Flow: flow, Execution: execution}))
	if err != nil {
		return domain.Link{}, rpcerr.FromConnect(err)
	}
	return pbconv.LinkFromPB(r.Msg.Link), nil
}

// ImpactLinkDelete implements engine.GraphPort.
func (c *Client) ImpactLinkDelete(ctx context.Context, id domain.ChangeID, link domain.LinkID, flow, execution string) error {
	_, err := c.rpc.ImpactLinkDelete(ctx, connect.NewRequest(&graphv1.ImpactLinkDeleteRequest{ChangeId: string(id), LinkId: string(link), Flow: flow, Execution: execution}))
	return rpcerr.FromConnect(err)
}

// ImpactNodeTransition implements engine.GraphPort.
func (c *Client) ImpactNodeTransition(ctx context.Context, id domain.ChangeID, in graph.NodeTransition) (domain.ChangeImpact, error) {
	r, err := c.rpc.ImpactNodeTransition(ctx, connect.NewRequest(&graphv1.ImpactNodeTransitionRequest{ChangeId: string(id), ChangeImpactId: string(in.Impact), NodeId: string(in.Node),
		State: in.To, Rationale: in.Rationale, Flow: in.Flow, Execution: in.Execution}))
	if err != nil {
		return domain.ChangeImpact{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactFromPB(r.Msg.Node), nil
}

// ImpactNodeCancel implements engine.GraphPort.
func (c *Client) ImpactNodeCancel(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) (domain.ChangeImpact, error) {
	r, err := c.rpc.ImpactNodeCancel(ctx, connect.NewRequest(&graphv1.ImpactNodeCancelRequest{ChangeId: string(id), ChangeImpactId: string(impact), Flow: flow, Execution: execution}))
	if err != nil {
		return domain.ChangeImpact{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactFromPB(r.Msg.Node), nil
}

// WithdrawImpact implements engine.GraphPort.
func (c *Client) WithdrawImpact(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) error {
	_, err := c.rpc.WithdrawImpact(ctx, connect.NewRequest(&graphv1.WithdrawImpactRequest{ChangeId: string(id), ChangeImpactId: string(impact), Flow: flow, Execution: execution}))
	return rpcerr.FromConnect(err)
}

// ImpactNodeReviewOn implements engine.GraphPort (the reviewer is the principal of the request).
func (c *Client) ImpactNodeReviewOn(ctx context.Context, id domain.ChangeID, flow, execution string, node domain.ChangeImpactID, status domain.NodeReview, _, comment string) (domain.ChangeImpact, error) {
	r, err := c.rpc.ImpactNodeReview(ctx, connect.NewRequest(&graphv1.ImpactNodeReviewRequest{ChangeId: string(id), ChangeImpactId: string(node),
		Accept: status == domain.ReviewAccepted, Comment: comment, Flow: flow, Execution: execution}))
	if err != nil {
		return domain.ChangeImpact{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactFromPB(r.Msg.Node), nil
}

// ImpactNodeReviewBatch implements engine.GraphPort (the reviewer is the principal of the request, ADR 0080).
func (c *Client) ImpactNodeReviewBatch(ctx context.Context, id domain.ChangeID, b domain.ReviewBatch) ([]domain.ChangeImpact, error) {
	req := &graphv1.ImpactNodeReviewBatchRequest{ChangeId: string(id), ReviewId: b.ID, Flow: b.Flow, Execution: b.Execution}
	for _, v := range b.Verdicts {
		req.Verdicts = append(req.Verdicts, &graphv1.ImpactVerdict{ChangeImpactId: string(v.Impact), Accept: v.Status == domain.ReviewAccepted, Comment: v.Comment})
	}
	if b.Item != nil {
		req.Item = pbconv.ItemToPB(*b.Item)
	}
	r, err := c.rpc.ImpactNodeReviewBatch(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	out := make([]domain.ChangeImpact, len(r.Msg.Nodes))
	for i, n := range r.Msg.Nodes {
		out[i] = pbconv.ChangeImpactFromPB(n)
	}
	return out, nil
}

// ReopenImpacts implements engine.GraphPort.
func (c *Client) ReopenImpacts(ctx context.Context, id domain.ChangeID, impacts []domain.ChangeImpactID, comment string) ([]domain.ChangeImpactID, error) {
	req := &graphv1.ReopenChangeImpactsRequest{ChangeId: string(id), Comment: comment}
	for _, i := range impacts {
		req.ChangeImpactIds = append(req.ChangeImpactIds, string(i))
	}
	r, err := c.rpc.ReopenChangeImpacts(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	out := make([]domain.ChangeImpactID, len(r.Msg.Reopened))
	for i, s := range r.Msg.Reopened {
		out[i] = domain.ChangeImpactID(s)
	}
	return out, nil
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

// Change implements engine.GraphPort.
func (c *Client) Change(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	r, err := c.rpc.GetChange(ctx, connect.NewRequest(&graphv1.GetChangeRequest{Id: string(id)}))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
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
		Methodology: in.Methodology, Data: pbconv.Struct(in.Data), BaselineId: string(in.Baseline), BaselineName: in.BaselineName, Edits: pbconv.EditsToPB(in.Edits),
		OwnerOrg: in.OwnerOrg, ProjectId: in.ProjectID}))
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
	// the facets of the wire are the built-in ones: options, active option, decision points
	var options []domain.Flow
	for _, f := range r.Msg.Options {
		options = append(options, pbconv.FlowFromPB(f))
	}
	var points []domain.DecisionPoint
	for _, d := range r.Msg.DecisionPoints {
		points = append(points, pbconv.DecisionPointFromPB(d))
	}
	bb := domain.Blackboard{Change: pbconv.ChangeFromPB(r.Msg.Change), Nodes: map[domain.NodeRef]domain.NodeView{}, Neighbors: map[domain.NodeRef]domain.Node{},
		Facets: map[string]any{domain.FacetOptions: options, domain.FacetActiveOption: r.Msg.ActiveOption, domain.FacetDecisionPoints: points},
		At:     pbconv.FromTime(r.Msg.At)}
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

// ChangeView implements engine.GraphPort.
func (c *Client) ChangeView(ctx context.Context, id domain.ChangeID, flow, level string) (domain.Baseline, error) {
	r, err := c.rpc.GetChangeView(ctx, connect.NewRequest(&graphv1.GetChangeViewRequest{ChangeId: string(id), Flow: flow, Level: level}))
	if err != nil {
		return domain.Baseline{}, rpcerr.FromConnect(err)
	}
	return pbconv.BaselineFromPB(r.Msg.Baseline), nil
}

// OpenOption implements engine.GraphPort (the principal comes from the request identity).
func (c *Client) OpenOption(ctx context.Context, id domain.ChangeID, in graph.OpenOptionRequest) (domain.Flow, error) {
	r, err := c.rpc.OpenOption(ctx, connect.NewRequest(&graphv1.OpenOptionRequest{ChangeId: string(id), Name: in.Name, Hypothesis: in.Hypothesis, Activate: in.Activate}))
	if err != nil {
		return domain.Flow{}, rpcerr.FromConnect(err)
	}
	return pbconv.FlowFromPB(r.Msg.Option), nil
}

// ActivateOption implements engine.GraphPort.
func (c *Client) ActivateOption(ctx context.Context, id domain.ChangeID, option, _ string) (string, error) {
	r, err := c.rpc.ActivateOption(ctx, connect.NewRequest(&graphv1.ActivateOptionRequest{ChangeId: string(id), Option: option}))
	if err != nil {
		return "", rpcerr.FromConnect(err)
	}
	return r.Msg.Active, nil
}

// EvaluateOption implements engine.GraphPort.
func (c *Client) EvaluateOption(ctx context.Context, id domain.ChangeID, option, _, comment string) (domain.Flow, error) {
	r, err := c.rpc.EvaluateOption(ctx, connect.NewRequest(&graphv1.EvaluateOptionRequest{ChangeId: string(id), Option: option, Comment: comment}))
	if err != nil {
		return domain.Flow{}, rpcerr.FromConnect(err)
	}
	return pbconv.FlowFromPB(r.Msg.Option), nil
}

// Options implements engine.GraphPort.
func (c *Client) Options(ctx context.Context, id domain.ChangeID) ([]domain.Flow, error) {
	r, err := c.rpc.ListOptions(ctx, connect.NewRequest(&graphv1.ListOptionsRequest{ChangeId: string(id)}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	var out []domain.Flow
	for _, f := range r.Msg.Options {
		out = append(out, pbconv.FlowFromPB(f))
	}
	return out, nil
}

// CompareOptions implements engine.GraphPort.
func (c *Client) CompareOptions(ctx context.Context, id domain.ChangeID, level string, all bool) (graph.OptionComparison, error) {
	r, err := c.rpc.CompareOptions(ctx, connect.NewRequest(&graphv1.CompareOptionsRequest{ChangeId: string(id), Level: level, All: all}))
	if err != nil {
		return graph.OptionComparison{}, rpcerr.FromConnect(err)
	}
	out := graph.OptionComparison{Level: r.Msg.Level}
	for _, f := range r.Msg.Options {
		out.Options = append(out.Options, pbconv.FlowFromPB(f))
	}
	for _, n := range r.Msg.Nodes {
		on := graph.OptionNode{Node: domain.NodeID(n.Node), Key: n.Key, Type: n.Type, Main: pbconv.RefPtrFromPB(n.Main),
			Options: map[string]*domain.NodeRef{}, Props: map[string]map[string]any{}}
		for _, f := range out.Options {
			on.Options[f.ID] = nil
		}
		for oid, ref := range n.Options {
			on.Options[oid] = pbconv.RefPtrFromPB(ref)
		}
		for side, p := range n.Props {
			on.Props[side] = pbconv.Map(p)
		}
		out.Nodes = append(out.Nodes, on)
	}
	return out, nil
}

// ChangeGraph implements engine.GraphPort.
func (c *Client) ChangeGraph(ctx context.Context, id domain.ChangeID, flow string) ([]domain.Node, []domain.Link, error) {
	r, err := c.rpc.GetChangeGraph(ctx, connect.NewRequest(&graphv1.GetChangeGraphRequest{ChangeId: string(id), Flow: flow}))
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

// TransitionChange moves the state of a change along a transition of its lifecycle (ADR 0058).
func (c *Client) TransitionChange(ctx context.Context, id domain.ChangeID, in graph.TransitionRequest) (domain.Change, error) {
	r, err := c.rpc.TransitionChange(ctx, connect.NewRequest(&graphv1.TransitionChangeRequest{ChangeId: string(id), Transition: in.Transition, Decision: in.Decision}))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
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

// AppendLog appends entries to the logs of their changes (graph.Graph.AppendLog).
func (c *Client) AppendLog(ctx context.Context, entries []domain.LogEntry) error {
	req := &graphv1.AppendLogRequest{}
	for _, e := range entries {
		req.Entries = append(req.Entries, pbconv.LogEntryToPB(e))
	}
	_, err := c.rpc.AppendLog(ctx, connect.NewRequest(req))
	return rpcerr.FromConnect(err)
}

// ChangeLog returns the entries of a change's log matching f, and the number of entries of each type matching f
// without its types (graph.Graph.ChangeLog).
func (c *Client) ChangeLog(ctx context.Context, f domain.LogFilter) ([]domain.LogEntry, map[string]int, error) {
	req := &graphv1.ListChangeLogRequest{ChangeId: string(f.Change), Types: f.Types, ProcessIds: f.Processes, Execution: f.Execution,
		AfterSeq: f.AfterSeq, Limit: int32(f.Limit)}
	for _, fl := range f.Flows {
		if fl == "" {
			fl = "main"
		}
		req.Flows = append(req.Flows, fl)
	}
	r, err := c.rpc.ListChangeLog(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, nil, rpcerr.FromConnect(err)
	}
	counts := map[string]int{}
	for t, n := range r.Msg.Counts {
		counts[t] = int(n)
	}
	return pbconv.LogEntriesFromPB(r.Msg.Entries), counts, nil
}

// Structures returns the structures of the graph (ADR 0054).
func (c *Client) Structures(ctx context.Context) (domain.Structures, error) {
	r, err := c.rpc.GetStructures(ctx, connect.NewRequest(&graphv1.GetStructuresRequest{}))
	if err != nil {
		return domain.Structures{}, rpcerr.FromConnect(err)
	}
	return pbconv.StructuresFromPB(r.Msg), nil
}

// AdminOnlyType tells whether a node type is written by platform administrators only (ADR 0068); a graph that
// cannot be reached answers true, so that nothing slips through the gate.
func (c *Client) AdminOnlyType(ctx context.Context, typ string) (bool, error) {
	r, err := c.rpc.IsAdminOnlyType(ctx, connect.NewRequest(&graphv1.IsAdminOnlyTypeRequest{Type: typ}))
	if err != nil {
		return true, rpcerr.FromConnect(err)
	}
	return r.Msg.AdminOnly, nil
}

func (c *Client) BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error) {
	r, err := c.rpc.GetBranch(ctx, connect.NewRequest(&graphv1.GetBranchRequest{Namespace: namespace, Name: name}))
	if err != nil {
		return domain.Baseline{}, rpcerr.FromConnect(err)
	}
	return pbconv.BaselineFromPB(r.Msg.Head), nil
}

// TagChange names the state an applied change leaves (ADR 0056).
func (c *Client) TagChange(ctx context.Context, id domain.ChangeID, name string) (domain.Tag, error) {
	r, err := c.rpc.TagChange(ctx, connect.NewRequest(&graphv1.TagChangeRequest{ChangeId: string(id), Name: name}))
	if err != nil {
		return domain.Tag{}, rpcerr.FromConnect(err)
	}
	return pbconv.TagFromPB(r.Msg.Tag), nil
}

// Tags lists the tags matching f.
func (c *Client) Tags(ctx context.Context, f domain.TagFilter) ([]domain.Tag, error) {
	r, err := c.rpc.ListTags(ctx, connect.NewRequest(&graphv1.ListTagsRequest{Namespace: f.Namespace, Name: f.Name, ChangeId: string(f.Change)}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	out := make([]domain.Tag, len(r.Msg.Tags))
	for i, t := range r.Msg.Tags {
		out[i] = pbconv.TagFromPB(t)
	}
	return out, nil
}

// DeleteTag removes a tag.
func (c *Client) DeleteTag(ctx context.Context, id domain.TagID) error {
	_, err := c.rpc.DeleteTag(ctx, connect.NewRequest(&graphv1.DeleteTagRequest{Id: string(id)}))
	return rpcerr.FromConnect(err)
}

// OpenFlow implements engine.GraphPort.
func (c *Client) OpenFlow(ctx context.Context, id domain.ChangeID, in graph.OpenFlowRequest) (domain.Flow, error) {
	r, err := c.rpc.OpenFlow(ctx, connect.NewRequest(&graphv1.OpenFlowRequest{ChangeId: string(id), Parent: in.Parent, ForkAfter: string(in.ForkAfter),
		Seeds: seedsToPB(in.Seeds), StaleRuns: in.StaleRuns, Origin: pbconv.Struct(in.Origin), Items: pbconv.ItemsToPB(in.Items)}))
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

// OpenDecision implements engine.GraphPort (the principal comes from the request identity).
func (c *Client) OpenDecision(ctx context.Context, id domain.ChangeID, in graph.OpenDecisionRequest) (domain.DecisionPoint, error) {
	req := &graphv1.OpenDecisionRequest{ChangeId: string(id), Question: in.Question, Options: in.Options, AllOptions: in.Options == nil,
		Criteria: in.Criteria, Policy: pbconv.Struct(in.Policy)}
	r, err := c.rpc.OpenDecision(ctx, connect.NewRequest(req))
	if err != nil {
		return domain.DecisionPoint{}, rpcerr.FromConnect(err)
	}
	return pbconv.DecisionPointFromPB(r.Msg.Point), nil
}

// RuleDecision implements engine.GraphPort.
func (c *Client) RuleDecision(ctx context.Context, id domain.ChangeID, in graph.RuleRequest) (domain.DecisionPoint, error) {
	r, err := c.rpc.RuleDecision(ctx, connect.NewRequest(&graphv1.RuleDecisionRequest{ChangeId: string(id), Point: in.Point, Outcome: in.Outcome,
		Option: in.Option, Confidence: in.Confidence, Justification: in.Justification, Questions: in.Questions, Agent: !in.Human}))
	if err != nil {
		return domain.DecisionPoint{}, rpcerr.FromConnect(err)
	}
	return pbconv.DecisionPointFromPB(r.Msg.Point), nil
}

// AnswerQuestion implements engine.GraphPort.
func (c *Client) AnswerQuestion(ctx context.Context, id domain.ChangeID, question, answer, process, _ string) (domain.DecisionPoint, error) {
	r, err := c.rpc.AnswerQuestion(ctx, connect.NewRequest(&graphv1.AnswerQuestionRequest{ChangeId: string(id), Question: question, Answer: answer, Process: process}))
	if err != nil {
		return domain.DecisionPoint{}, rpcerr.FromConnect(err)
	}
	return pbconv.DecisionPointFromPB(r.Msg.Point), nil
}

// RatifyDecision implements engine.GraphPort.
func (c *Client) RatifyDecision(ctx context.Context, id domain.ChangeID, point string, accept bool, _, comment string) (domain.DecisionPoint, error) {
	r, err := c.rpc.RatifyDecision(ctx, connect.NewRequest(&graphv1.RatifyDecisionRequest{ChangeId: string(id), Point: point, Accept: accept, Comment: comment}))
	if err != nil {
		return domain.DecisionPoint{}, rpcerr.FromConnect(err)
	}
	return pbconv.DecisionPointFromPB(r.Msg.Point), nil
}

// DecisionPoints implements engine.GraphPort.
func (c *Client) DecisionPoints(ctx context.Context, id domain.ChangeID) ([]domain.DecisionPoint, error) {
	r, err := c.rpc.ListDecisionPoints(ctx, connect.NewRequest(&graphv1.ListDecisionPointsRequest{ChangeId: string(id)}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	var out []domain.DecisionPoint
	for _, d := range r.Msg.Points {
		out = append(out, pbconv.DecisionPointFromPB(d))
	}
	return out, nil
}
