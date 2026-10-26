// Package graphclient is the client of the graph service: engine.GraphPort and the reads of the other services over
// RPC. It names the graph through its contract (pkg/graphapi) and the generated code only, so a service calling the
// graph does not import the graph nor its service (pkg/layering, ADR 0125).
package graphclient

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/pbconv"
	"github.com/zimwip/goap/lib/rpcerr"
)

// Client adapts the graph Connect client to engine.GraphPort (checked by client_port_test.go: the graph service names
// no engine type, pkg/layering).
type Client struct {
	rpc graphv1connect.GraphServiceClient
}

// NewClient returns a client for the graph service at baseURL.
// The requests carry the principal of their context: the engine acts for the initiator of a process, which the graph
// records on the events of the change impacts (ADR 0029).
//
// A request whose context carries no principal (a directory refreshing its snapshot in the background) goes as the
// service client, a named principal with no role: the graph refuses anonymous requests (Handler.Identify).
func NewClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: graphv1connect.NewGraphServiceClient(hc, baseURL, append([]connect.ClientOption{identity.Forward(), serviceIdentity(), positions()}, opts...)...)}
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

// positions carries the positions of the logs of the changes (ADR 0153): the ones the context requires of the reads
// (graphapi.AtLeast) go with the request, the ones the writes reached come back into its watermark
// (graphapi.WithWatermark).
func positions() connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if m := graphapi.AllAtLeast(ctx); len(m) > 0 {
				req.Header().Set(graphapi.HeaderAtLeast, graphapi.FormatPositions(m))
			}
			res, err := next(ctx, req)
			w := graphapi.WatermarkOf(ctx)
			if w == nil {
				return res, err
			}
			v := ""
			if res != nil {
				v = res.Header().Get(graphapi.HeaderChangeSeq)
			} else if ce := new(connect.Error); errors.As(err, &ce) {
				v = ce.Meta().Get(graphapi.HeaderChangeSeq)
			}
			for id, seq := range graphapi.ParsePositions(v) {
				w.Note(id, seq)
			}
			return res, err
		}
	}))
}

// DeclareUser makes sure the User node of a subject exists (ADR 0042: a user is declared the moment they sign
// in), for a caller with no graph in process (the gateway). It reads that node as the subject: the service's
// EnsureCaller interceptor creates it first when missing (EnsureUser, member of the unit new users join), so
// finding it proves it was created, and not finding it reports why it could not be.
func (c *Client) DeclareUser(ctx context.Context, subject string) error {
	ctx = authz.With(ctx, authz.Principal{Subject: subject})
	_, err := c.rpc.GetNode(ctx, connect.NewRequest(&graphv1.GetNodeRequest{View: &graphv1.View{Namespace: access.NamespaceOrganisation, Branch: domain.MainBranch}, Key: access.UserKey(subject)}))
	return rpcerr.FromConnect(err)
}

func (c *Client) CreateChange(ctx context.Context, in graphapi.NewChange) (domain.Change, error) {
	r, err := c.rpc.CreateChange(ctx, connect.NewRequest(&graphv1.CreateChangeRequest{Title: in.Title, Intent: in.Intent,
		Methodology: in.Methodology, MethodologyVersion: in.MethodologyVersion, Namespace: in.Namespace, BaselineId: string(in.BaselineID), Branch: in.Branch, OwnBranch: in.OwnBranch, ParentId: string(in.ParentID), OwnerOrg: in.OwnerOrg, ProjectId: in.ProjectID, Data: pbconv.Struct(in.Data)}))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
}

// MoveChange moves a root change, with its open sub-changes, to another project (ADR 0091).
func (c *Client) MoveChange(ctx context.Context, id domain.ChangeID, project string) (domain.Change, error) {
	r, err := c.rpc.MoveChange(ctx, connect.NewRequest(&graphv1.MoveChangeRequest{ChangeId: string(id), ProjectId: project}))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
}

// AttachChange implements engine.GraphPort (ADR 0106).
func (c *Client) AttachChange(ctx context.Context, id, parent domain.ChangeID) (domain.Change, error) {
	r, err := c.rpc.AttachChange(ctx, connect.NewRequest(&graphv1.AttachChangeRequest{ChangeId: string(id), ParentId: string(parent)}))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
}

// DetachChange implements engine.GraphPort (ADR 0106).
func (c *Client) DetachChange(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	r, err := c.rpc.DetachChange(ctx, connect.NewRequest(&graphv1.DetachChangeRequest{ChangeId: string(id)}))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
}

// SetChangeDependencies implements engine.GraphPort (ADR 0108).
func (c *Client) SetChangeDependencies(ctx context.Context, id domain.ChangeID, deps []domain.ChangeID) (domain.Change, error) {
	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = string(d)
	}
	r, err := c.rpc.SetChangeDependencies(ctx, connect.NewRequest(&graphv1.SetChangeDependenciesRequest{ChangeId: string(id), DependsOn: ids}))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
}

// UpgradeChange implements engine.GraphPort (ADR 0113).
func (c *Client) UpgradeChange(ctx context.Context, id domain.ChangeID, version string) (domain.Change, error) {
	r, err := c.rpc.UpgradeChange(ctx, connect.NewRequest(&graphv1.UpgradeChangeRequest{ChangeId: string(id), Version: version}))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
}

// ChangeDependencies implements engine.GraphPort (ADR 0108).
func (c *Client) ChangeDependencies(ctx context.Context, id domain.ChangeID) (graphapi.ChangeDependencies, error) {
	r, err := c.rpc.GetChangeDependencies(ctx, connect.NewRequest(&graphv1.GetChangeDependenciesRequest{ChangeId: string(id)}))
	if err != nil {
		return graphapi.ChangeDependencies{}, rpcerr.FromConnect(err)
	}
	return graphapi.ChangeDependencies{DependsOn: pbconv.DependenciesFromPB(r.Msg.DependsOn), Dependents: pbconv.DependenciesFromPB(r.Msg.Dependents),
		Order: pbconv.DependenciesFromPB(r.Msg.Order)}, nil
}

func (c *Client) UpdateChange(ctx context.Context, id domain.ChangeID, p graphapi.ChangePatch) (domain.Change, error) {
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
func (c *Client) ImpactNodeCreate(ctx context.Context, id domain.ChangeID, in graphapi.NodeCreate) (domain.ChangeImpact, error) {
	req := &graphv1.ImpactNodeCreateRequest{ChangeId: string(id), Key: in.Key, Type: in.Type, Props: pbconv.Struct(in.Properties), Owner: in.Owner,
		Rationale: in.Rationale, Flow: in.Flow, Execution: in.Execution}
	for _, l := range in.Links {
		req.Links = append(req.Links, &graphv1.NodeLinkWrite{Type: l.Type, To: pbconv.RefToPB(l.To), Target: pbconv.TargetToPB(l.Target), Props: pbconv.Struct(l.Properties)})
	}
	r, err := c.rpc.ImpactNodeCreate(ctx, connect.NewRequest(req))
	if err != nil {
		return domain.ChangeImpact{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactFromPB(r.Msg.Node), nil
}

func nameToPB(n graphapi.NodeName) *graphv1.NodeName {
	return &graphv1.NodeName{ChangeImpactId: string(n.Impact), NodeId: string(n.Node), Key: n.Key}
}

func specToPB(c graphapi.NodeCreate) *graphv1.NodeCreateSpec {
	s := &graphv1.NodeCreateSpec{Key: c.Key, Type: c.Type, Props: pbconv.Struct(c.Properties), Owner: c.Owner, Rationale: c.Rationale}
	for _, l := range c.Links {
		s.Links = append(s.Links, &graphv1.NodeLinkWrite{Type: l.Type, To: pbconv.RefToPB(l.To), Target: pbconv.TargetToPB(l.Target), Props: pbconv.Struct(l.Properties)})
	}
	return s
}

// ImpactNodeMerge implements engine.GraphPort (ADR 0077). The Gate of the input is the handler's here.
func (c *Client) ImpactNodeMerge(ctx context.Context, id domain.ChangeID, in graphapi.MergeInput) (graphapi.Restructured, error) {
	req := &graphv1.ImpactNodeMergeRequest{ChangeId: string(id), Into: specToPB(in.Into), Rationale: in.Rationale, Flow: in.Flow, Execution: in.Execution}
	for _, s := range in.Sources {
		req.Sources = append(req.Sources, nameToPB(s))
	}
	r, err := c.rpc.ImpactNodeMerge(ctx, connect.NewRequest(req))
	if err != nil {
		return graphapi.Restructured{}, rpcerr.FromConnect(err)
	}
	return pbconv.RestructuredFromPB(r.Msg), nil
}

// ImpactNodeSplit implements engine.GraphPort (ADR 0077).
func (c *Client) ImpactNodeSplit(ctx context.Context, id domain.ChangeID, in graphapi.SplitInput) (graphapi.Restructured, error) {
	req := &graphv1.ImpactNodeSplitRequest{ChangeId: string(id), Source: nameToPB(in.Source), Rationale: in.Rationale, Flow: in.Flow, Execution: in.Execution}
	for _, s := range in.Into {
		req.Into = append(req.Into, specToPB(s))
	}
	r, err := c.rpc.ImpactNodeSplit(ctx, connect.NewRequest(req))
	if err != nil {
		return graphapi.Restructured{}, rpcerr.FromConnect(err)
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
func (c *Client) ImpactNodeCheckout(ctx context.Context, id domain.ChangeID, in graphapi.NodeCheckout) (domain.ChangeImpact, error) {
	r, err := c.rpc.ImpactNodeCheckout(ctx, connect.NewRequest(&graphv1.ImpactNodeCheckoutRequest{ChangeId: string(id), ChangeImpactId: string(in.Impact), NodeId: string(in.Node),
		Rationale: in.Rationale, Flow: in.Flow, Execution: in.Execution}))
	if err != nil {
		return domain.ChangeImpact{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactFromPB(r.Msg.Node), nil
}

// ImpactNodeCheckoutBatch checks out several nodes at once, all or none, on the flow and execution of the first.
func (c *Client) ImpactNodeCheckoutBatch(ctx context.Context, id domain.ChangeID, nodes []graphapi.NodeCheckout) ([]domain.ChangeImpact, error) {
	req := &graphv1.ImpactNodeCheckoutBatchRequest{ChangeId: string(id)}
	if len(nodes) > 0 {
		req.Flow, req.Execution = nodes[0].Flow, nodes[0].Execution
	}
	for _, n := range nodes {
		req.Nodes = append(req.Nodes, &graphv1.NodeCheckoutSpec{ChangeImpactId: string(n.Impact), NodeId: string(n.Node), Key: n.Key, Rationale: n.Rationale})
	}
	r, err := c.rpc.ImpactNodeCheckoutBatch(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	out := make([]domain.ChangeImpact, 0, len(r.Msg.Nodes))
	for _, n := range r.Msg.Nodes {
		out = append(out, pbconv.ChangeImpactFromPB(n))
	}
	return out, nil
}

// ImpactNodeUpdate implements engine.GraphPort.
func (c *Client) ImpactNodeUpdate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, in graphapi.NodeUpdate) (domain.ChangeImpact, error) {
	r, err := c.rpc.ImpactNodeUpdate(ctx, connect.NewRequest(&graphv1.ImpactNodeUpdateRequest{ChangeId: string(id), ChangeImpactId: string(impact), Props: pbconv.Struct(in.Properties),
		Owner: in.Owner, Flow: in.Flow, Execution: in.Execution}))
	if err != nil {
		return domain.ChangeImpact{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactFromPB(r.Msg.Node), nil
}

// ImpactLinkCreate implements engine.GraphPort.
func (c *Client) ImpactLinkCreate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, l graphapi.LinkWrite, flow, execution string) (domain.Link, error) {
	r, err := c.rpc.ImpactLinkCreate(ctx, connect.NewRequest(&graphv1.ImpactLinkCreateRequest{ChangeId: string(id), ChangeImpactId: string(impact), Type: l.Type, To: pbconv.RefToPB(l.To), Target: pbconv.TargetToPB(l.Target),
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
func (c *Client) ImpactNodeTransition(ctx context.Context, id domain.ChangeID, in graphapi.NodeTransition) (domain.ChangeImpact, error) {
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

// RebaseChange brings a sub-change up to date with its parent (ADR 0082), a root change with the head of its target branch (ADR 0101).
func (c *Client) RebaseChange(ctx context.Context, id domain.ChangeID) (graphapi.RebaseResult, error) {
	r, err := c.rpc.RebaseChange(ctx, connect.NewRequest(&graphv1.RebaseChangeRequest{ChangeId: string(id)}))
	if err != nil {
		return graphapi.RebaseResult{}, rpcerr.FromConnect(err)
	}
	out := graphapi.RebaseResult{Parent: domain.ChangeID(r.Msg.ParentId), Branch: r.Msg.Branch, From: domain.BaselineID(r.Msg.From), Onto: domain.BaselineID(r.Msg.Onto)}
	for _, i := range r.Msg.Impacts {
		out.Impacts = append(out.Impacts, graphapi.RebasedImpact{Impact: domain.ChangeImpactID(i.ChangeImpactId), Key: i.Key, Changed: i.Changed, Merged: i.Merged, Conflicts: i.Conflicts})
	}
	return out, nil
}

// ImpactNodeResolve keeps the sub-change's values for the conflicts a rebase left on a change impact (ADR 0082).
func (c *Client) ImpactNodeResolve(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, execution string) (domain.ChangeImpact, error) {
	r, err := c.rpc.ImpactNodeResolve(ctx, connect.NewRequest(&graphv1.ImpactNodeResolveRequest{ChangeId: string(id), ChangeImpactId: string(impact), Execution: execution}))
	if err != nil {
		return domain.ChangeImpact{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeImpactFromPB(r.Msg.Node), nil
}

// RebaseState tells where a change stands against its parent or its target branch (ADR 0082, 0101).
func (c *Client) RebaseState(ctx context.Context, id domain.ChangeID) (graphapi.RebaseState, error) {
	r, err := c.rpc.GetRebaseState(ctx, connect.NewRequest(&graphv1.GetRebaseStateRequest{ChangeId: string(id)}))
	if err != nil {
		return graphapi.RebaseState{}, rpcerr.FromConnect(err)
	}
	out := graphapi.RebaseState{Parent: domain.ChangeID(r.Msg.ParentId), Branch: r.Msg.Branch, Onto: domain.BaselineID(r.Msg.Onto)}
	for _, id := range r.Msg.BehindImpactIds {
		out.Behind = append(out.Behind, domain.ChangeImpactID(id))
	}
	for _, x := range r.Msg.Conflicts {
		out.Conflicts = append(out.Conflicts, graphapi.ImpactConflicts{Impact: domain.ChangeImpactID(x.ChangeImpactId), Key: x.Key, Conflicts: x.Conflicts})
	}
	return out, nil
}

// WithdrawImpact implements engine.GraphPort.
func (c *Client) WithdrawImpact(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) error {
	_, err := c.rpc.WithdrawImpact(ctx, connect.NewRequest(&graphv1.WithdrawImpactRequest{ChangeId: string(id), ChangeImpactId: string(impact), Flow: flow, Execution: execution}))
	return rpcerr.FromConnect(err)
}

// WithdrawImpacts takes several change impacts out of the change at once (all or none).
func (c *Client) WithdrawImpacts(ctx context.Context, id domain.ChangeID, impacts []domain.ChangeImpactID, flow, execution string) error {
	ids := make([]string, len(impacts))
	for i, impact := range impacts {
		ids[i] = string(impact)
	}
	_, err := c.rpc.WithdrawImpacts(ctx, connect.NewRequest(&graphv1.WithdrawImpactsRequest{ChangeId: string(id), ChangeImpactIds: ids, Flow: flow, Execution: execution}))
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

// ChangeAt reads a change as its log stood at a position (graph.Graph.ChangeAt, ADR 0153).
func (c *Client) ChangeAt(ctx context.Context, id domain.ChangeID, seq int64) (domain.Change, error) {
	r, err := c.rpc.GetChange(ctx, connect.NewRequest(&graphv1.GetChangeRequest{Id: string(id), AtSeq: seq}))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
}

// ListChanges implements engine.GraphPort.
func (c *Client) ListChanges(ctx context.Context, f graphapi.ChangesFilter) ([]domain.Change, error) {
	req := &graphv1.ListChangesRequest{Namespace: f.Namespace, OwnerOrg: f.OwnerOrg, HeaderOnly: f.HeadersOnly, Project: f.Project, Query: f.Query}
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

// Commit runs a change of node edits in the graph service (see graphapi.Commit).
func (c *Client) Commit(ctx context.Context, in graphapi.Commit) (graphapi.CommitResult, error) {
	r, err := c.rpc.CommitEdits(ctx, connect.NewRequest(&graphv1.CommitEditsRequest{Namespace: in.Namespace, Title: in.Title, Intent: in.Intent,
		Methodology: in.Methodology, Data: pbconv.Struct(in.Data), BaselineId: string(in.Baseline), BaselineName: in.BaselineName, Edits: pbconv.EditsToPB(in.Edits),
		OwnerOrg: in.OwnerOrg, ProjectId: in.ProjectID, Events: carriedToPB(in.Events)}))
	if err != nil {
		return graphapi.CommitResult{}, rpcerr.FromConnect(err)
	}
	return graphapi.CommitResult{Change: domain.ChangeID(r.Msg.ChangeId), Baseline: pbconv.BaselineFromPB(r.Msg.Baseline)}, nil
}

func carriedToPB(evs []graphapi.CarriedEvent) []*graphv1.CarriedEvent {
	var out []*graphv1.CarriedEvent
	for _, e := range evs {
		out = append(out, &graphv1.CarriedEvent{Subject: e.Subject, Payload: e.Payload})
	}
	return out
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

// BaselineDelta reads what moved from a baseline to a later one of its branch (graphsnap.DeltaReader, ADR 0143 §4).
func (c *Client) BaselineDelta(ctx context.Context, from, to domain.BaselineID) (graphapi.BaselineDelta, error) {
	r, err := c.rpc.GetBaselineDelta(ctx, connect.NewRequest(&graphv1.GetBaselineDeltaRequest{From: string(from), To: string(to)}))
	if err != nil {
		return graphapi.BaselineDelta{}, rpcerr.FromConnect(err)
	}
	d := graphapi.BaselineDelta{From: pbconv.BaselineFromPB(r.Msg.From), To: pbconv.BaselineFromPB(r.Msg.To),
		Nodes: pbconv.NodesFromPB(r.Msg.Nodes), Links: pbconv.LinksFromPB(r.Msg.Links)}
	for _, id := range r.Msg.Removed {
		d.Removed = append(d.Removed, domain.NodeID(id))
	}
	return d, nil
}

// SuspectLinks returns the links whose source is in the baseline and whose target is there at another version (the
// suspect links GetBaselineGraph answers with).
func (c *Client) SuspectLinks(ctx context.Context, id domain.BaselineID) ([]domain.Link, error) {
	_, _, sus, err := c.BaselineGraphWithSuspects(ctx, id)
	return sus, err
}

// BaselineGraphWithSuspects is BaselineGraph and SuspectLinks in one call.
func (c *Client) BaselineGraphWithSuspects(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, []domain.Link, error) {
	r, err := c.rpc.GetBaselineGraph(ctx, connect.NewRequest(&graphv1.GetBaselineGraphRequest{Id: string(id)}))
	if err != nil {
		return nil, nil, nil, rpcerr.FromConnect(err)
	}
	return pbconv.NodesFromPB(r.Msg.Nodes), pbconv.LinksFromPB(r.Msg.Links), pbconv.LinksFromPB(r.Msg.SuspectLinks), nil
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
func (c *Client) OpenOption(ctx context.Context, id domain.ChangeID, in graphapi.OpenOptionRequest) (domain.Flow, error) {
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

// DiffFlows compares the impacts two flows of a change see (ADR 0083).
func (c *Client) DiffFlows(ctx context.Context, id domain.ChangeID, left, right, level string) (domain.FlowDiff, error) {
	r, err := c.rpc.DiffFlows(ctx, connect.NewRequest(&graphv1.DiffFlowsRequest{ChangeId: string(id), Left: left, Right: right, Level: level}))
	if err != nil {
		return domain.FlowDiff{}, rpcerr.FromConnect(err)
	}
	return pbconv.FlowDiffFromPB(id, r.Msg), nil
}

// CompareOptions implements engine.GraphPort.
func (c *Client) CompareOptions(ctx context.Context, id domain.ChangeID, level string, all bool) (graphapi.OptionComparison, error) {
	r, err := c.rpc.CompareOptions(ctx, connect.NewRequest(&graphv1.CompareOptionsRequest{ChangeId: string(id), Level: level, All: all}))
	if err != nil {
		return graphapi.OptionComparison{}, rpcerr.FromConnect(err)
	}
	out := graphapi.OptionComparison{Level: r.Msg.Level}
	for _, f := range r.Msg.Options {
		out.Options = append(out.Options, pbconv.FlowFromPB(f))
	}
	for _, n := range r.Msg.Nodes {
		on := graphapi.OptionNode{Node: domain.NodeID(n.Node), Key: n.Key, Type: n.Type, Main: pbconv.RefPtrFromPB(n.Main),
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

// CommitChange implements engine.GraphPort: the header of the commit.
func (c *Client) CommitChange(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	r, err := c.rpc.CommitChange(ctx, connect.NewRequest(&graphv1.CommitChangeRequest{ChangeId: string(id), BaselineName: baselineName}))
	if err != nil {
		return domain.Baseline{}, rpcerr.FromConnect(err)
	}
	return pbconv.BaselineFromPB(r.Msg.Baseline), nil
}

// CommitChangeNodes is a partial commit (graph.CommitChangeNodes): the header of the commit.
func (c *Client) CommitChangeNodes(ctx context.Context, id domain.ChangeID, baselineName string, nodes []domain.NodeID) (domain.Baseline, error) {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = string(n)
	}
	r, err := c.rpc.CommitChange(ctx, connect.NewRequest(&graphv1.CommitChangeRequest{ChangeId: string(id), BaselineName: baselineName, Nodes: ids}))
	if err != nil {
		return domain.Baseline{}, rpcerr.FromConnect(err)
	}
	return pbconv.BaselineFromPB(r.Msg.Baseline), nil
}

// Branch implements engine.GraphPort.
func (c *Client) Branch(ctx context.Context, namespace, name string) (domain.Branch, error) {
	r, err := c.rpc.GetBranch(ctx, connect.NewRequest(&graphv1.GetBranchRequest{Namespace: namespace, Name: name}))
	if err != nil {
		return domain.Branch{}, rpcerr.FromConnect(err)
	}
	return pbconv.BranchFromPB(r.Msg.Branch), nil
}

// CreateBranch opens a branch forked from a baseline.
func (c *Client) CreateBranch(ctx context.Context, in graphapi.NewBranch) (domain.Branch, error) {
	r, err := c.rpc.CreateBranch(ctx, connect.NewRequest(&graphv1.CreateBranchRequest{Name: in.Name, Namespace: in.Namespace,
		FromBaseline: string(in.From), Origin: in.Origin, Description: in.Description}))
	if err != nil {
		return domain.Branch{}, rpcerr.FromConnect(err)
	}
	return pbconv.BranchFromPB(r.Msg.Branch), nil
}

// VersionPage gives a page of the history of a node: its last limit versions below before (0: up to its latest), by
// version, and the before of the next older page (0: none).
func (c *Client) VersionPage(ctx context.Context, id domain.NodeID, before domain.Version, limit int) ([]domain.Node, domain.Version, error) {
	r, err := c.rpc.ListNodeVersions(ctx, connect.NewRequest(&graphv1.ListNodeVersionsRequest{Id: string(id), Before: int32(before), Limit: int32(limit)}))
	if err != nil {
		return nil, 0, rpcerr.FromConnect(err)
	}
	return pbconv.NodesFromPB(r.Msg.Versions), domain.Version(r.Msg.Next), nil
}

// PlanMerge implements engine.GraphPort.
func (c *Client) PlanMerge(ctx context.Context, namespace, from, into string) (graphapi.MergePlan, error) {
	r, err := c.rpc.PlanMerge(ctx, connect.NewRequest(&graphv1.PlanMergeRequest{Namespace: namespace, From: from, Into: into}))
	if err != nil {
		return graphapi.MergePlan{}, rpcerr.FromConnect(err)
	}
	return pbconv.MergePlanFromPB(r.Msg.Plan), nil
}

// IntegrateChange implements engine.GraphPort (MergeChange over RPC).
func (c *Client) IntegrateChange(ctx context.Context, id domain.ChangeID, resolutions map[domain.NodeID]graphapi.Resolution) (domain.Change, error) {
	r, err := c.rpc.MergeChange(ctx, connect.NewRequest(&graphv1.MergeChangeRequest{ChangeId: string(id), Resolutions: pbconv.ResolutionsToPB(resolutions)}))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
}

// CloseChange implements engine.GraphPort (CloseChange over RPC).
func (c *Client) CloseChange(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	r, err := c.rpc.CloseChange(ctx, connect.NewRequest(&graphv1.CloseChangeRequest{ChangeId: string(id)}))
	if err != nil {
		return domain.Change{}, rpcerr.FromConnect(err)
	}
	return pbconv.ChangeFromPB(r.Msg.Change), nil
}

// TransitionChange moves the state of a change along a transition of its lifecycle (ADR 0058).
func (c *Client) TransitionChange(ctx context.Context, id domain.ChangeID, in graphapi.TransitionRequest) (domain.Change, error) {
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

// BranchHeadHeader is the header of the head of a branch: its id and order, never its state.
func (c *Client) BranchHeadHeader(ctx context.Context, namespace, name string) (domain.Baseline, error) {
	r, err := c.rpc.GetBranch(ctx, connect.NewRequest(&graphv1.GetBranchRequest{Namespace: namespace, Name: name, HeaderOnly: true}))
	if err != nil {
		return domain.Baseline{}, rpcerr.FromConnect(err)
	}
	return pbconv.BaselineFromPB(r.Msg.Head), nil
}

// TagVersion names a node version (ADR 0143 §6).
func (c *Client) TagVersion(ctx context.Context, ref domain.NodeRef, name string) (domain.Tag, error) {
	r, err := c.rpc.TagVersion(ctx, connect.NewRequest(&graphv1.TagVersionRequest{NodeId: string(ref.ID), Version: int32(ref.Version), Name: name}))
	if err != nil {
		return domain.Tag{}, rpcerr.FromConnect(err)
	}
	return pbconv.TagFromPB(r.Msg.Tag), nil
}

// Tags lists the tags matching f.
func (c *Client) Tags(ctx context.Context, f domain.TagFilter) ([]domain.Tag, error) {
	r, err := c.rpc.ListTags(ctx, connect.NewRequest(&graphv1.ListTagsRequest{Namespace: f.Namespace, Name: f.Name, NodeId: string(f.Node)}))
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
func (c *Client) OpenFlow(ctx context.Context, id domain.ChangeID, in graphapi.OpenFlowRequest) (domain.Flow, error) {
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
func (c *Client) OpenDecision(ctx context.Context, id domain.ChangeID, in graphapi.OpenDecisionRequest) (domain.DecisionPoint, error) {
	req := &graphv1.OpenDecisionRequest{ChangeId: string(id), Question: in.Question, Options: in.Options, AllOptions: in.Options == nil,
		Criteria: in.Criteria, Policy: pbconv.Struct(in.Policy), Choices: in.Choices}
	r, err := c.rpc.OpenDecision(ctx, connect.NewRequest(req))
	if err != nil {
		return domain.DecisionPoint{}, rpcerr.FromConnect(err)
	}
	return pbconv.DecisionPointFromPB(r.Msg.Point), nil
}

// RuleDecision implements engine.GraphPort.
func (c *Client) RuleDecision(ctx context.Context, id domain.ChangeID, in graphapi.RuleRequest) (domain.DecisionPoint, error) {
	r, err := c.rpc.RuleDecision(ctx, connect.NewRequest(&graphv1.RuleDecisionRequest{ChangeId: string(id), Point: in.Point, Outcome: in.Outcome,
		Option: in.Option, Confidence: in.Confidence, Justification: in.Justification, Questions: in.Questions, Agent: !in.Human, Role: in.Role}))
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

// ViewVersions answers what a view holds of some nodes (ADR 0157).
func (c *Client) ViewVersions(ctx context.Context, v graphapi.View, ids []domain.NodeID) (graphapi.ViewState, error) {
	req := &graphv1.ResolveViewRequest{View: pbconv.GraphViewToPB(v)}
	for _, id := range ids {
		req.NodeIds = append(req.NodeIds, string(id))
	}
	r, err := c.rpc.ResolveView(ctx, connect.NewRequest(req))
	if err != nil {
		return graphapi.ViewState{}, rpcerr.FromConnect(err)
	}
	st := graphapi.ViewState{Namespace: r.Msg.Namespace, Baseline: domain.BaselineID(r.Msg.BaselineId), MainHead: r.Msg.MainHead,
		Versions: map[domain.NodeID]domain.Version{}}
	for id, v := range r.Msg.Versions {
		st.Versions[domain.NodeID(id)] = domain.Version(v)
	}
	return st, nil
}

// ViewDrafts returns the drafts a change view holds matching a text, at most limit (ADR 0157): the version 0 nodes of a
// page of the view.
func (c *Client) ViewDrafts(ctx context.Context, v graphapi.View, text string, limit int) ([]domain.Node, error) {
	if !v.IsChange() {
		return nil, nil
	}
	r, err := c.rpc.ListBaselineNodes(ctx, connect.NewRequest(&graphv1.ListBaselineNodesRequest{View: pbconv.GraphViewToPB(v), Query: text, Limit: 500}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	var out []domain.Node
	for _, n := range r.Msg.Nodes {
		if n.Version == 0 && (limit <= 0 || len(out) < limit) {
			out = append(out, pbconv.NodeFromPB(n))
		}
	}
	return out, nil
}

// ViewNode reads a node as a view holds it, by id or, with no id, by key (ADR 0157).
func (c *Client) ViewNode(ctx context.Context, v graphapi.View, id domain.NodeID, key string) (domain.NodeView, error) {
	req := &graphv1.GetNodeRequest{View: pbconv.GraphViewToPB(v), Key: key, LinkLimit: maxLinkPage}
	if id != "" {
		req.Key, req.Ref = "", &graphv1.NodeRef{Id: string(id)}
	}
	r, err := c.rpc.GetNode(ctx, connect.NewRequest(req))
	if err != nil {
		return domain.NodeView{}, rpcerr.FromConnect(err)
	}
	// the port's view holds every link: the pages GetNode did not answer
	pv := r.Msg.View
	at := &graphv1.NodeRef{Id: pv.GetNode().GetId()}
	for _, d := range []struct {
		incoming bool
		links    *[]*graphv1.Link
		total    int32
	}{{false, &pv.Out, pv.OutTotal}, {true, &pv.In, pv.InTotal}} {
		for int32(len(*d.links)) < d.total {
			p, err := c.rpc.ListNodeLinks(ctx, connect.NewRequest(&graphv1.ListNodeLinksRequest{Ref: at, View: req.View, Incoming: d.incoming,
				Offset: int32(len(*d.links)), Limit: maxLinkPage}))
			if err != nil {
				return domain.NodeView{}, rpcerr.FromConnect(err)
			}
			if len(p.Msg.Links) == 0 {
				break
			}
			*d.links = append(*d.links, p.Msg.Links...)
		}
	}
	return pbconv.ViewFromPB(pv), nil
}

// maxLinkPage is the largest page of links the graph service answers.
const maxLinkPage = 500

// ViewNeighbourhood reads a node as a view holds it, with its direct neighbours (ADR 0157).
func (c *Client) ViewNeighbourhood(ctx context.Context, v graphapi.View, id domain.NodeID) (graphapi.Neighbourhood, error) {
	r, err := c.rpc.GetNodeNeighbourhood(ctx, connect.NewRequest(&graphv1.GetNodeNeighbourhoodRequest{View: pbconv.GraphViewToPB(v), NodeId: string(id)}))
	if err != nil {
		return graphapi.Neighbourhood{}, rpcerr.FromConnect(err)
	}
	nb := graphapi.Neighbourhood{Node: pbconv.NodeFromPB(r.Msg.Node)}
	for _, n := range r.Msg.Nodes {
		nb.Nodes = append(nb.Nodes, pbconv.NodeFromPB(n))
	}
	for _, l := range r.Msg.Links {
		nb.Links = append(nb.Links, pbconv.LinkFromPB(l))
	}
	for _, id := range r.Msg.SuspectLinkIds {
		nb.Suspect = append(nb.Suspect, domain.LinkID(id))
	}
	return nb, nil
}

// ViewNodes reads a page of the nodes of a view (ADR 0157).
func (c *Client) ViewNodes(ctx context.Context, v graphapi.View, q graphapi.NodeQuery) (graphapi.NodePage, error) {
	r, err := c.rpc.ListBaselineNodes(ctx, connect.NewRequest(&graphv1.ListBaselineNodesRequest{View: pbconv.GraphViewToPB(v), Type: q.Type,
		Query: q.Text, Offset: int32(q.Offset), Limit: int32(q.Limit), IncludeDeleted: q.IncludeDeleted, ChangeId: string(q.Change)}))
	if err != nil {
		return graphapi.NodePage{}, rpcerr.FromConnect(err)
	}
	page := graphapi.NodePage{Total: int(r.Msg.Total), Baseline: domain.BaselineID(r.Msg.GetBaseline().GetId())}
	for _, n := range r.Msg.Nodes {
		page.Nodes = append(page.Nodes, pbconv.NodeFromPB(n))
	}
	for _, t := range r.Msg.Types {
		page.Types = append(page.Types, graphapi.TypeCount{Type: t.Type, Count: int(t.Count)})
	}
	return page, nil
}

// ViewNodesOfType reads every node of a type a view holds (ADR 0157).
func (c *Client) ViewNodesOfType(ctx context.Context, v graphapi.View, typ string) ([]domain.Node, error) {
	r, err := c.rpc.ListViewNodes(ctx, connect.NewRequest(&graphv1.ListViewNodesRequest{View: pbconv.GraphViewToPB(v), Type: typ}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	out := make([]domain.Node, 0, len(r.Msg.Nodes))
	for _, n := range r.Msg.Nodes {
		out = append(out, pbconv.NodeFromPB(n))
	}
	return out, nil
}

// QueryView runs a read query in a view (ADR 0160) over the RPC; the caps are the service's (the options give the
// bound variables only).
func (c *Client) QueryView(ctx context.Context, v graphapi.View, query string, opts graphapi.QueryOptions) (graphapi.QueryResult, error) {
	req := &graphv1.QueryViewRequest{View: pbconv.GraphViewToPB(v), Query: query}
	for name, id := range opts.Bind {
		if req.Bind == nil {
			req.Bind = map[string]string{}
		}
		req.Bind[name] = string(id)
	}
	r, err := c.rpc.QueryView(ctx, connect.NewRequest(req))
	if err != nil {
		return graphapi.QueryResult{}, rpcerr.FromConnect(err)
	}
	out := graphapi.QueryResult{Columns: r.Msg.Columns, Links: pbconv.LinksFromPB(r.Msg.Links), Truncated: r.Msg.Truncated, Notes: r.Msg.Notes,
		Reads: int(r.Msg.Reads), Baseline: domain.BaselineID(r.Msg.BaselineId)}
	for _, n := range r.Msg.Nodes {
		out.Nodes = append(out.Nodes, pbconv.NodeFromPB(n))
	}
	for _, row := range r.Msg.Rows {
		out.Rows = append(out.Rows, row.AsSlice())
	}
	return out, nil
}
