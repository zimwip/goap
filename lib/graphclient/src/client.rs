//! Package graphclient is the client of the graph service: graphapi::Port (the engine's GraphPort) and the reads of
//! the other services over RPC. It names the graph through its contract (goap-graphapi) and the generated code only,
//! so a service calling the graph does not import the graph nor its service (platform/layering, ADR 0125).

use std::collections::BTreeMap;
use std::sync::{Arc, LazyLock};

use goap_authz::{self as authz, Principal};
use goap_connect::{
    AnyRequest, AnyResponse, ClientBuilder, ConnectError, Interceptor, Next, Request, async_trait,
};
use goap_ctx::Ctx;
use goap_domain::{
    self as domain, Baseline, BaselineID, Blackboard, BoardIssue, Branch, Change, ChangeID,
    ChangeImpact, ChangeImpactID, ChangeItem, DecisionPoint, Flow, FlowDiff, Link, LinkID,
    LogEntry, LogFilter, Node, NodeID, NodeRef, NodeReview, ReviewBatch, Structures, Tag,
    TagFilter, TagID,
};
use goap_gen::graph::v1::{self as pb, GraphService, GraphServiceClient};
use goap_graphapi::{self as graphapi, Error};
use goap_identity as identity;
use goap_pbconv as pbconv;
use goap_rpcerr::from_connect;
use serde_json::{Map, Value};

/// ClientPrincipal is the identity of a graph client acting for no one in particular.
pub static CLIENT_PRINCIPAL: LazyLock<Principal> = LazyLock::new(|| authz::system("client", &[]));

/// The interceptor naming a request that carries no principal as the service client ([`CLIENT_PRINCIPAL`]).
#[derive(Debug, Clone, Copy, Default)]
pub struct ServiceIdentity;

#[async_trait]
impl Interceptor for ServiceIdentity {
    async fn unary(
        &self,
        ctx: Ctx,
        mut req: AnyRequest,
        next: Next<'_>,
    ) -> Result<AnyResponse, ConnectError> {
        if req.spec.is_client && req.header(identity::HEADER_SUBJECT).is_empty() {
            identity::set_headers(&CLIENT_PRINCIPAL, &mut req.headers);
        }
        next.run(ctx, req).await
    }
}

/// Client adapts the graph Connect client to graphapi::Port (the engine's GraphPort; the graph service names no
/// engine type, platform/layering).
#[derive(Clone)]
pub struct Client {
    rpc: Arc<dyn GraphService>,
}

fn r<T>(res: Result<goap_connect::Response<T>, ConnectError>) -> Result<T, Error> {
    res.map(|r| r.into_inner()).map_err(from_connect)
}

fn change_of(c: Option<pb::Change>) -> Change {
    pbconv::opt_change_from_pb(c.as_ref())
}

fn impact_of(n: Option<pb::ChangeImpact>) -> ChangeImpact {
    n.as_ref()
        .map(pbconv::change_impact_from_pb)
        .unwrap_or_default()
}

fn baseline_of(b: Option<pb::Baseline>) -> Baseline {
    b.as_ref().map(pbconv::baseline_from_pb).unwrap_or_default()
}

fn link_of(l: Option<pb::Link>) -> Link {
    l.as_ref().map(pbconv::link_from_pb).unwrap_or_default()
}

fn flow_of(f: Option<pb::Flow>) -> Flow {
    pbconv::flow_from_pb(f.as_ref())
}

fn point_of(d: Option<pb::DecisionPoint>) -> DecisionPoint {
    pbconv::decision_point_from_pb(d.as_ref())
}

fn ids<T: AsRef<str>>(ids: &[T]) -> Vec<String> {
    ids.iter().map(|i| i.as_ref().to_string()).collect()
}

fn link_writes(ls: &[graphapi::LinkWrite]) -> Vec<pb::NodeLinkWrite> {
    ls.iter()
        .map(|l| pb::NodeLinkWrite {
            r#type: l.r#type.clone(),
            to: Some(pbconv::ref_to_pb(&l.to)),
            props: pbconv::to_struct(&l.properties),
        })
        .collect()
}

fn name_to_pb(n: &graphapi::NodeName) -> pb::NodeName {
    pb::NodeName {
        change_impact_id: n.impact.to_string(),
        node_id: n.node.to_string(),
        key: n.key.clone(),
    }
}

fn spec_to_pb(c: &graphapi::NodeCreate) -> pb::NodeCreateSpec {
    pb::NodeCreateSpec {
        key: c.key.clone(),
        r#type: c.r#type.clone(),
        props: pbconv::to_struct(&c.properties),
        owner: c.owner.clone(),
        rationale: c.rationale.clone(),
        links: link_writes(&c.links),
    }
}

impl Client {
    /// NewClient returns a client for the graph service at `base_url`.
    ///
    /// The requests carry the principal of their context: the engine acts for the initiator of a process, which the
    /// graph records on the events of the change impacts (ADR 0029). A request whose context carries no principal
    /// (a directory refreshing its snapshot in the background) goes as the service client, a named principal with
    /// no role: the graph refuses anonymous requests (Handler.Identify).
    pub fn new(base_url: &str) -> Self {
        Self::from_connect(Self::builder(base_url).build())
    }

    /// The builder of the Connect client of `new`: the identity interceptors set, the caller adding its own
    /// settings (inner to them, as Go's options after them).
    pub fn builder(base_url: &str) -> ClientBuilder {
        goap_connect::Client::builder(base_url)
            .interceptor(identity::Forward)
            .interceptor(ServiceIdentity)
    }

    /// A client over a Connect client built by the caller (from [`Client::builder`] to keep the identity).
    pub fn from_connect(c: goap_connect::Client) -> Self {
        Self::with_service(Arc::new(GraphServiceClient::new(c)))
    }

    /// A client over any implementation of the service (the handler in process, a test).
    pub fn with_service(rpc: Arc<dyn GraphService>) -> Self {
        Client { rpc }
    }

    /// DeclareUser makes sure the User node of a subject exists (ADR 0042: a user is declared the moment they sign
    /// in), for a caller with no graph in process (the gateway). It reads that node as the subject: the service's
    /// EnsureCaller interceptor creates it first when missing (EnsureUser, member of the unit new users join), so
    /// finding it proves it was created, and not finding it reports why it could not be.
    pub async fn declare_user(&self, ctx: &Ctx, subject: &str) -> Result<(), Error> {
        let ctx = authz::with_principal(
            ctx,
            Principal {
                subject: subject.to_string(),
                ..Default::default()
            },
        );
        r(self
            .rpc
            .get_node(
                &ctx,
                Request::new(pb::GetNodeRequest {
                    namespace: goap_access::NAMESPACE_ORGANISATION.to_string(),
                    key: goap_access::user_key(subject),
                    ..Default::default()
                }),
            )
            .await)
        .map(|_| ())
    }

    /// DerivedNodes returns the nodes that derive from a node (ADR 0077).
    pub async fn derived_nodes(&self, ctx: &Ctx, rf: &NodeRef) -> Result<Vec<Node>, Error> {
        let m = r(self
            .rpc
            .derived_nodes(
                ctx,
                Request::new(pb::DerivedNodesRequest {
                    r#ref: Some(pbconv::ref_to_pb(rf)),
                }),
            )
            .await)?;
        Ok(pbconv::nodes_from_pb(&m.nodes))
    }

    /// ImpactLinkUpdate replaces the properties of a link of a draft.
    pub async fn impact_link_update(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        link: &LinkID,
        props: &Map<String, Value>,
        flow: &str,
        execution: &str,
    ) -> Result<Link, Error> {
        let m = r(self
            .rpc
            .impact_link_update(
                ctx,
                Request::new(pb::ImpactLinkUpdateRequest {
                    change_id: id.to_string(),
                    link_id: link.to_string(),
                    props: pbconv::to_struct(props),
                    flow: flow.to_string(),
                    execution: execution.to_string(),
                }),
            )
            .await)?;
        Ok(link_of(m.link))
    }

    /// RebaseChange brings a sub-change up to date with its parent (ADR 0082), a root change with the head of its
    /// target branch (ADR 0101).
    pub async fn rebase_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
    ) -> Result<graphapi::RebaseResult, Error> {
        let m = r(self
            .rpc
            .rebase_change(
                ctx,
                Request::new(pb::RebaseChangeRequest {
                    change_id: id.to_string(),
                }),
            )
            .await)?;
        Ok(graphapi::RebaseResult {
            parent: ChangeID::new(m.parent_id),
            branch: m.branch,
            from: BaselineID::new(m.from),
            onto: BaselineID::new(m.onto),
            impacts: m
                .impacts
                .into_iter()
                .map(|i| graphapi::RebasedImpact {
                    impact: ChangeImpactID::new(i.change_impact_id),
                    key: i.key,
                    changed: i.changed,
                    merged: i.merged,
                    conflicts: i.conflicts,
                })
                .collect(),
        })
    }

    /// ImpactNodeResolve keeps the sub-change's values for the conflicts a rebase left on a change impact (ADR 0082).
    pub async fn impact_node_resolve(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        execution: &str,
    ) -> Result<ChangeImpact, Error> {
        let m = r(self
            .rpc
            .impact_node_resolve(
                ctx,
                Request::new(pb::ImpactNodeResolveRequest {
                    change_id: id.to_string(),
                    change_impact_id: impact.to_string(),
                    execution: execution.to_string(),
                }),
            )
            .await)?;
        Ok(impact_of(m.node))
    }

    /// Changes lists every change known to the graph service.
    pub async fn changes(&self, ctx: &Ctx) -> Result<Vec<Change>, Error> {
        let m = r(self
            .rpc
            .list_changes(ctx, Request::new(pb::ListChangesRequest::default()))
            .await)?;
        Ok(m.changes.iter().map(pbconv::change_from_pb).collect())
    }

    /// Commit runs a change of node edits in the graph service (see graphapi::Commit).
    pub async fn commit(
        &self,
        ctx: &Ctx,
        input: graphapi::Commit,
    ) -> Result<graphapi::CommitResult, Error> {
        let m = r(self
            .rpc
            .commit_edits(
                ctx,
                Request::new(pb::CommitEditsRequest {
                    namespace: input.namespace,
                    title: input.title,
                    intent: input.intent,
                    methodology: input.methodology,
                    data: pbconv::to_struct(&input.data),
                    baseline_id: input.baseline.to_string(),
                    baseline_name: input.baseline_name,
                    edits: pbconv::edits_to_pb(&input.edits),
                    owner_org: input.owner_org,
                    project_id: input.project_id,
                }),
            )
            .await)?;
        Ok(graphapi::CommitResult {
            change: ChangeID::new(m.change_id),
            baseline: baseline_of(m.baseline),
        })
    }

    /// SuspectLinks returns the links whose source is in the baseline and whose target is there at another version
    /// (the suspect links GetBaselineGraph answers with).
    pub async fn suspect_links(&self, ctx: &Ctx, id: &BaselineID) -> Result<Vec<Link>, Error> {
        let (_, _, sus) = self.baseline_graph_with_suspects(ctx, id).await?;
        Ok(sus)
    }

    /// BaselineGraphWithSuspects is BaselineGraph and SuspectLinks in one call.
    pub async fn baseline_graph_with_suspects(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>, Vec<Link>), Error> {
        let m = r(self
            .rpc
            .get_baseline_graph(
                ctx,
                Request::new(pb::GetBaselineGraphRequest { id: id.to_string() }),
            )
            .await)?;
        Ok((
            pbconv::nodes_from_pb(&m.nodes),
            pbconv::links_from_pb(&m.links),
            pbconv::links_from_pb(&m.suspect_links),
        ))
    }

    /// DiffFlows compares the impacts two flows of a change see (ADR 0083).
    pub async fn diff_flows(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        left: &str,
        right: &str,
        level: &str,
    ) -> Result<FlowDiff, Error> {
        let m = r(self
            .rpc
            .diff_flows(
                ctx,
                Request::new(pb::DiffFlowsRequest {
                    change_id: id.to_string(),
                    left: left.to_string(),
                    right: right.to_string(),
                    level: level.to_string(),
                }),
            )
            .await)?;
        Ok(pbconv::flow_diff_from_pb(id.clone(), &m))
    }

    /// TagChange names the state an applied change leaves (ADR 0056).
    pub async fn tag_change(&self, ctx: &Ctx, id: &ChangeID, name: &str) -> Result<Tag, Error> {
        let m = r(self
            .rpc
            .tag_change(
                ctx,
                Request::new(pb::TagChangeRequest {
                    change_id: id.to_string(),
                    name: name.to_string(),
                }),
            )
            .await)?;
        Ok(m.tag.as_ref().map(pbconv::tag_from_pb).unwrap_or_default())
    }

    /// Tags lists the tags matching f.
    pub async fn tags(&self, ctx: &Ctx, f: &TagFilter) -> Result<Vec<Tag>, Error> {
        let m = r(self
            .rpc
            .list_tags(
                ctx,
                Request::new(pb::ListTagsRequest {
                    namespace: f.namespace.clone(),
                    name: f.name.clone(),
                    change_id: f.change.to_string(),
                }),
            )
            .await)?;
        Ok(m.tags.iter().map(pbconv::tag_from_pb).collect())
    }

    /// DeleteTag removes a tag.
    pub async fn delete_tag(&self, ctx: &Ctx, id: &TagID) -> Result<(), Error> {
        r(self
            .rpc
            .delete_tag(
                ctx,
                Request::new(pb::DeleteTagRequest { id: id.to_string() }),
            )
            .await)
        .map(|_| ())
    }
}

#[async_trait]
impl graphapi::Port for Client {
    async fn create_change(&self, ctx: &Ctx, in_: graphapi::NewChange) -> Result<Change, Error> {
        let m = r(self
            .rpc
            .create_change(
                ctx,
                Request::new(pb::CreateChangeRequest {
                    title: in_.title,
                    intent: in_.intent,
                    methodology: in_.methodology,
                    methodology_version: in_.methodology_version,
                    namespace: in_.namespace,
                    baseline_id: in_.baseline_id.to_string(),
                    branch: in_.branch,
                    own_branch: in_.own_branch,
                    parent_id: in_.parent_id.to_string(),
                    owner_org: in_.owner_org,
                    project_id: in_.project_id,
                    data: pbconv::to_struct(&in_.data),
                }),
            )
            .await)?;
        Ok(change_of(m.change))
    }

    async fn update_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        p: graphapi::ChangePatch,
    ) -> Result<Change, Error> {
        let m = r(self
            .rpc
            .update_change(
                ctx,
                Request::new(pb::UpdateChangeRequest {
                    id: id.to_string(),
                    title: p.title,
                    intent: p.intent,
                    goal: p.goal,
                    data: pbconv::to_struct(&p.data),
                    status: p.status.map(|s| s.to_string()),
                }),
            )
            .await)?;
        Ok(change_of(m.change))
    }

    /// MoveChange moves a root change, with its open sub-changes, to another project (ADR 0091).
    async fn move_change(&self, ctx: &Ctx, id: &ChangeID, project: &str) -> Result<Change, Error> {
        let m = r(self
            .rpc
            .move_change(
                ctx,
                Request::new(pb::MoveChangeRequest {
                    change_id: id.to_string(),
                    project_id: project.to_string(),
                }),
            )
            .await)?;
        Ok(change_of(m.change))
    }

    async fn attach_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        parent: &ChangeID,
    ) -> Result<Change, Error> {
        let m = r(self
            .rpc
            .attach_change(
                ctx,
                Request::new(pb::AttachChangeRequest {
                    change_id: id.to_string(),
                    parent_id: parent.to_string(),
                }),
            )
            .await)?;
        Ok(change_of(m.change))
    }

    async fn detach_change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, Error> {
        let m = r(self
            .rpc
            .detach_change(
                ctx,
                Request::new(pb::DetachChangeRequest {
                    change_id: id.to_string(),
                }),
            )
            .await)?;
        Ok(change_of(m.change))
    }

    async fn set_change_dependencies(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        deps: Vec<ChangeID>,
    ) -> Result<Change, Error> {
        let m = r(self
            .rpc
            .set_change_dependencies(
                ctx,
                Request::new(pb::SetChangeDependenciesRequest {
                    change_id: id.to_string(),
                    depends_on: ids(&deps),
                }),
            )
            .await)?;
        Ok(change_of(m.change))
    }

    async fn change_dependencies(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
    ) -> Result<graphapi::ChangeDependencies, Error> {
        let m = r(self
            .rpc
            .get_change_dependencies(
                ctx,
                Request::new(pb::GetChangeDependenciesRequest {
                    change_id: id.to_string(),
                }),
            )
            .await)?;
        Ok(graphapi::ChangeDependencies {
            depends_on: pbconv::dependencies_from_pb(&m.depends_on),
            dependents: pbconv::dependencies_from_pb(&m.dependents),
            order: pbconv::dependencies_from_pb(&m.order),
        })
    }

    async fn upgrade_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        version: &str,
    ) -> Result<Change, Error> {
        let m = r(self
            .rpc
            .upgrade_change(
                ctx,
                Request::new(pb::UpgradeChangeRequest {
                    change_id: id.to_string(),
                    version: version.to_string(),
                }),
            )
            .await)?;
        Ok(change_of(m.change))
    }

    async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, Error> {
        let m = r(self
            .rpc
            .get_change(
                ctx,
                Request::new(pb::GetChangeRequest { id: id.to_string() }),
            )
            .await)?;
        Ok(change_of(m.change))
    }

    async fn add_items(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        items: Vec<ChangeItem>,
    ) -> Result<Vec<ChangeItem>, Error> {
        let m = r(self
            .rpc
            .add_items(
                ctx,
                Request::new(pb::AddItemsRequest {
                    change_id: id.to_string(),
                    items: pbconv::items_to_pb(&items),
                }),
            )
            .await)?;
        Ok(pbconv::items_from_pb(&m.items))
    }

    async fn propose_impact(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        nodes: Vec<ChangeImpact>,
    ) -> Result<Vec<ChangeImpact>, Error> {
        let m = r(self
            .rpc
            .propose_impact(
                ctx,
                Request::new(pb::ProposeImpactRequest {
                    change_id: id.to_string(),
                    nodes: pbconv::change_impacts_to_pb(&nodes),
                }),
            )
            .await)?;
        Ok(pbconv::change_impacts_from_pb(&m.nodes))
    }

    /// ImpactNodeCreate creates a node in a change (ADR 0076).
    async fn impact_node_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: graphapi::NodeCreate,
    ) -> Result<ChangeImpact, Error> {
        let m = r(self
            .rpc
            .impact_node_create(
                ctx,
                Request::new(pb::ImpactNodeCreateRequest {
                    change_id: id.to_string(),
                    key: in_.key,
                    r#type: in_.r#type,
                    props: pbconv::to_struct(&in_.properties),
                    owner: in_.owner,
                    rationale: in_.rationale,
                    flow: in_.flow,
                    execution: in_.execution,
                    links: link_writes(&in_.links),
                }),
            )
            .await)?;
        Ok(impact_of(m.node))
    }

    async fn impact_node_checkout(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: graphapi::NodeCheckout,
    ) -> Result<ChangeImpact, Error> {
        let m = r(self
            .rpc
            .impact_node_checkout(
                ctx,
                Request::new(pb::ImpactNodeCheckoutRequest {
                    change_id: id.to_string(),
                    change_impact_id: in_.impact.to_string(),
                    node_id: in_.node.to_string(),
                    rationale: in_.rationale,
                    flow: in_.flow,
                    execution: in_.execution,
                }),
            )
            .await)?;
        Ok(impact_of(m.node))
    }

    async fn impact_node_update(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        in_: graphapi::NodeUpdate,
    ) -> Result<ChangeImpact, Error> {
        let m = r(self
            .rpc
            .impact_node_update(
                ctx,
                Request::new(pb::ImpactNodeUpdateRequest {
                    change_id: id.to_string(),
                    change_impact_id: impact.to_string(),
                    props: pbconv::to_struct(&in_.properties),
                    owner: in_.owner,
                    flow: in_.flow,
                    execution: in_.execution,
                }),
            )
            .await)?;
        Ok(impact_of(m.node))
    }

    async fn impact_link_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        l: graphapi::LinkWrite,
        flow: &str,
        execution: &str,
    ) -> Result<Link, Error> {
        let m = r(self
            .rpc
            .impact_link_create(
                ctx,
                Request::new(pb::ImpactLinkCreateRequest {
                    change_id: id.to_string(),
                    change_impact_id: impact.to_string(),
                    r#type: l.r#type,
                    to: Some(pbconv::ref_to_pb(&l.to)),
                    props: pbconv::to_struct(&l.properties),
                    flow: flow.to_string(),
                    execution: execution.to_string(),
                }),
            )
            .await)?;
        Ok(link_of(m.link))
    }

    async fn impact_link_delete(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        link: &LinkID,
        flow: &str,
        execution: &str,
    ) -> Result<(), Error> {
        r(self
            .rpc
            .impact_link_delete(
                ctx,
                Request::new(pb::ImpactLinkDeleteRequest {
                    change_id: id.to_string(),
                    link_id: link.to_string(),
                    flow: flow.to_string(),
                    execution: execution.to_string(),
                }),
            )
            .await)
        .map(|_| ())
    }

    async fn impact_node_transition(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: graphapi::NodeTransition,
    ) -> Result<ChangeImpact, Error> {
        let m = r(self
            .rpc
            .impact_node_transition(
                ctx,
                Request::new(pb::ImpactNodeTransitionRequest {
                    change_id: id.to_string(),
                    change_impact_id: in_.checkout.impact.to_string(),
                    node_id: in_.checkout.node.to_string(),
                    state: in_.to,
                    rationale: in_.checkout.rationale,
                    flow: in_.checkout.flow,
                    execution: in_.checkout.execution,
                }),
            )
            .await)?;
        Ok(impact_of(m.node))
    }

    async fn impact_node_cancel(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        flow: &str,
        execution: &str,
    ) -> Result<ChangeImpact, Error> {
        let m = r(self
            .rpc
            .impact_node_cancel(
                ctx,
                Request::new(pb::ImpactNodeCancelRequest {
                    change_id: id.to_string(),
                    change_impact_id: impact.to_string(),
                    flow: flow.to_string(),
                    execution: execution.to_string(),
                }),
            )
            .await)?;
        Ok(impact_of(m.node))
    }

    async fn withdraw_impact(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        flow: &str,
        execution: &str,
    ) -> Result<(), Error> {
        r(self
            .rpc
            .withdraw_impact(
                ctx,
                Request::new(pb::WithdrawImpactRequest {
                    change_id: id.to_string(),
                    change_impact_id: impact.to_string(),
                    flow: flow.to_string(),
                    execution: execution.to_string(),
                }),
            )
            .await)
        .map(|_| ())
    }

    /// WithdrawImpacts takes several change impacts out of the change at once (all or none).
    async fn withdraw_impacts(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impacts: Vec<ChangeImpactID>,
        flow: &str,
        execution: &str,
    ) -> Result<(), Error> {
        r(self
            .rpc
            .withdraw_impacts(
                ctx,
                Request::new(pb::WithdrawImpactsRequest {
                    change_id: id.to_string(),
                    change_impact_ids: ids(&impacts),
                    flow: flow.to_string(),
                    execution: execution.to_string(),
                }),
            )
            .await)
        .map(|_| ())
    }

    /// ImpactNodeMerge (ADR 0077). The gate of the input is the handler's here.
    async fn impact_node_merge(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: graphapi::MergeInput,
    ) -> Result<graphapi::Restructured, Error> {
        let m = r(self
            .rpc
            .impact_node_merge(
                ctx,
                Request::new(pb::ImpactNodeMergeRequest {
                    change_id: id.to_string(),
                    into: Some(spec_to_pb(&in_.into)),
                    rationale: in_.rationale,
                    flow: in_.flow,
                    execution: in_.execution,
                    sources: in_.sources.iter().map(name_to_pb).collect(),
                }),
            )
            .await)?;
        Ok(pbconv::restructured_from_pb(&m))
    }

    /// ImpactNodeSplit (ADR 0077).
    async fn impact_node_split(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: graphapi::SplitInput,
    ) -> Result<graphapi::Restructured, Error> {
        let m = r(self
            .rpc
            .impact_node_split(
                ctx,
                Request::new(pb::ImpactNodeSplitRequest {
                    change_id: id.to_string(),
                    source: Some(name_to_pb(&in_.source)),
                    rationale: in_.rationale,
                    flow: in_.flow,
                    execution: in_.execution,
                    into: in_.into.iter().map(spec_to_pb).collect(),
                }),
            )
            .await)?;
        Ok(pbconv::restructured_from_pb(&m))
    }

    /// ImpactNodeReviewOn (the reviewer is the principal of the request).
    async fn impact_node_review_on(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        execution: &str,
        node: &ChangeImpactID,
        status: NodeReview,
        _by: &str,
        comment: &str,
    ) -> Result<ChangeImpact, Error> {
        let m = r(self
            .rpc
            .impact_node_review(
                ctx,
                Request::new(pb::ImpactNodeReviewRequest {
                    change_id: id.to_string(),
                    change_impact_id: node.to_string(),
                    accept: status == domain::REVIEW_ACCEPTED,
                    comment: comment.to_string(),
                    flow: flow.to_string(),
                    execution: execution.to_string(),
                }),
            )
            .await)?;
        Ok(impact_of(m.node))
    }

    /// ImpactNodeReviewBatch (the reviewer is the principal of the request, ADR 0080).
    async fn impact_node_review_batch(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        b: ReviewBatch,
    ) -> Result<Vec<ChangeImpact>, Error> {
        let m = r(self
            .rpc
            .impact_node_review_batch(
                ctx,
                Request::new(pb::ImpactNodeReviewBatchRequest {
                    change_id: id.to_string(),
                    review_id: b.id,
                    flow: b.flow,
                    execution: b.execution,
                    verdicts: b
                        .verdicts
                        .iter()
                        .map(|v| pb::ImpactVerdict {
                            change_impact_id: v.impact.to_string(),
                            accept: v.status == domain::REVIEW_ACCEPTED,
                            comment: v.comment.clone(),
                        })
                        .collect(),
                    item: b.item.as_ref().map(pbconv::item_to_pb),
                }),
            )
            .await)?;
        Ok(pbconv::change_impacts_from_pb(&m.nodes))
    }

    async fn reopen_impacts(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impacts: Vec<ChangeImpactID>,
        comment: &str,
    ) -> Result<Vec<ChangeImpactID>, Error> {
        let m = r(self
            .rpc
            .reopen_change_impacts(
                ctx,
                Request::new(pb::ReopenChangeImpactsRequest {
                    change_id: id.to_string(),
                    comment: comment.to_string(),
                    change_impact_ids: ids(&impacts),
                }),
            )
            .await)?;
        Ok(m.reopened.into_iter().map(ChangeImpactID::new).collect())
    }

    async fn blackboard(&self, ctx: &Ctx, id: &ChangeID) -> Result<Blackboard, Error> {
        self.blackboard_in(ctx, id, "").await
    }

    /// BlackboardIn is the blackboard seen from a flow branch.
    async fn blackboard_in(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<Blackboard, Error> {
        let m = r(self
            .rpc
            .get_blackboard(
                ctx,
                Request::new(pb::GetBlackboardRequest {
                    change_id: id.to_string(),
                    flow: flow.to_string(),
                }),
            )
            .await)?;
        // the facets of the wire are the built-in ones: options, active option, decision points
        let options: Vec<Flow> = m
            .options
            .iter()
            .map(|f| pbconv::flow_from_pb(Some(f)))
            .collect();
        let points: Vec<DecisionPoint> = m
            .decision_points
            .iter()
            .map(|d| pbconv::decision_point_from_pb(Some(d)))
            .collect();
        let mut bb = Blackboard {
            change: pbconv::opt_change_from_pb(m.change.as_ref()),
            at: pbconv::from_opt_time(m.at.as_ref()),
            ..Default::default()
        };
        bb.facets
            .insert(domain::FACET_OPTIONS.to_string(), Arc::new(options));
        bb.facets.insert(
            domain::FACET_ACTIVE_OPTION.to_string(),
            Arc::new(m.active_option.clone()),
        );
        bb.facets
            .insert(domain::FACET_DECISION_POINTS.to_string(), Arc::new(points));
        for v in &m.nodes {
            let nv = pbconv::view_from_pb(v);
            bb.nodes.insert(nv.r#ref(), nv);
        }
        for n in &m.neighbors {
            let nn = pbconv::node_from_pb(n);
            bb.neighbors.insert(nn.r#ref(), nn);
        }
        Ok(bb)
    }

    async fn open_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: graphapi::OpenFlowRequest,
    ) -> Result<Flow, Error> {
        let m = r(self
            .rpc
            .open_flow(
                ctx,
                Request::new(pb::OpenFlowRequest {
                    change_id: id.to_string(),
                    parent: in_.parent,
                    fork_after: in_.fork_after.to_string(),
                    seeds: ids(&in_.seeds),
                    stale_runs: in_.stale_runs,
                    origin: pbconv::to_struct(&in_.origin),
                    items: pbconv::items_to_pb(&in_.items),
                }),
            )
            .await)?;
        Ok(flow_of(m.flow))
    }

    /// AdoptFlow (the adopting principal comes from the request identity).
    async fn adopt_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        _by: &str,
    ) -> Result<Flow, Error> {
        let m = r(self
            .rpc
            .adopt_flow(
                ctx,
                Request::new(pb::AdoptFlowRequest {
                    change_id: id.to_string(),
                    flow: flow.to_string(),
                }),
            )
            .await)?;
        Ok(flow_of(m.flow))
    }

    async fn discard_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        _by: &str,
    ) -> Result<Flow, Error> {
        let m = r(self
            .rpc
            .discard_flow(
                ctx,
                Request::new(pb::DiscardFlowRequest {
                    change_id: id.to_string(),
                    flow: flow.to_string(),
                }),
            )
            .await)?;
        Ok(flow_of(m.flow))
    }

    async fn validate_board(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<Vec<BoardIssue>, Error> {
        let m = r(self
            .rpc
            .validate_board(
                ctx,
                Request::new(pb::ValidateBoardRequest {
                    change_id: id.to_string(),
                    flow: flow.to_string(),
                }),
            )
            .await)?;
        Ok(m.issues.iter().map(pbconv::board_issue_from_pb).collect())
    }

    async fn baseline_graph(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>), Error> {
        let (nodes, links, _) = self.baseline_graph_with_suspects(ctx, id).await?;
        Ok((nodes, links))
    }

    async fn change_graph(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<(Vec<Node>, Vec<Link>), Error> {
        let m = r(self
            .rpc
            .get_change_graph(
                ctx,
                Request::new(pb::GetChangeGraphRequest {
                    change_id: id.to_string(),
                    flow: flow.to_string(),
                }),
            )
            .await)?;
        Ok((
            pbconv::nodes_from_pb(&m.nodes),
            pbconv::links_from_pb(&m.links),
        ))
    }

    async fn change_view(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        level: &str,
    ) -> Result<Baseline, Error> {
        let m = r(self
            .rpc
            .get_change_view(
                ctx,
                Request::new(pb::GetChangeViewRequest {
                    change_id: id.to_string(),
                    flow: flow.to_string(),
                    level: level.to_string(),
                }),
            )
            .await)?;
        Ok(baseline_of(m.baseline))
    }

    /// OpenOption (the principal comes from the request identity).
    async fn open_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: graphapi::OpenOptionRequest,
    ) -> Result<Flow, Error> {
        let m = r(self
            .rpc
            .open_option(
                ctx,
                Request::new(pb::OpenOptionRequest {
                    change_id: id.to_string(),
                    name: in_.name,
                    hypothesis: in_.hypothesis,
                    activate: in_.activate,
                }),
            )
            .await)?;
        Ok(flow_of(m.option))
    }

    async fn activate_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        _by: &str,
    ) -> Result<String, Error> {
        let m = r(self
            .rpc
            .activate_option(
                ctx,
                Request::new(pb::ActivateOptionRequest {
                    change_id: id.to_string(),
                    option: option.to_string(),
                }),
            )
            .await)?;
        Ok(m.active)
    }

    async fn evaluate_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        _by: &str,
        comment: &str,
    ) -> Result<Flow, Error> {
        let m = r(self
            .rpc
            .evaluate_option(
                ctx,
                Request::new(pb::EvaluateOptionRequest {
                    change_id: id.to_string(),
                    option: option.to_string(),
                    comment: comment.to_string(),
                }),
            )
            .await)?;
        Ok(flow_of(m.option))
    }

    async fn options(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<Flow>, Error> {
        let m = r(self
            .rpc
            .list_options(
                ctx,
                Request::new(pb::ListOptionsRequest {
                    change_id: id.to_string(),
                }),
            )
            .await)?;
        Ok(m.options
            .iter()
            .map(|f| pbconv::flow_from_pb(Some(f)))
            .collect())
    }

    async fn compare_options(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        level: &str,
        all: bool,
    ) -> Result<graphapi::OptionComparison, Error> {
        let m = r(self
            .rpc
            .compare_options(
                ctx,
                Request::new(pb::CompareOptionsRequest {
                    change_id: id.to_string(),
                    level: level.to_string(),
                    all,
                }),
            )
            .await)?;
        let options: Vec<Flow> = m
            .options
            .iter()
            .map(|f| pbconv::flow_from_pb(Some(f)))
            .collect();
        let nodes = m
            .nodes
            .iter()
            .map(|n| {
                let mut on = graphapi::OptionNode {
                    node: NodeID::new(n.node.clone()),
                    key: n.key.clone(),
                    r#type: n.r#type.clone(),
                    main: pbconv::ref_ptr_from_pb(n.main.as_ref()),
                    options: options.iter().map(|f| (f.id.clone(), None)).collect(),
                    props: BTreeMap::new(),
                };
                for (oid, rf) in &n.options {
                    on.options
                        .insert(oid.clone(), pbconv::ref_ptr_from_pb(Some(rf)));
                }
                for (side, p) in &n.props {
                    on.props.insert(side.clone(), pbconv::to_map(Some(p)));
                }
                on
            })
            .collect();
        Ok(graphapi::OptionComparison {
            level: m.level,
            options,
            nodes,
        })
    }

    /// OpenDecision (the principal comes from the request identity).
    async fn open_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: graphapi::OpenDecisionRequest,
    ) -> Result<DecisionPoint, Error> {
        let m = r(self
            .rpc
            .open_decision(
                ctx,
                Request::new(pb::OpenDecisionRequest {
                    change_id: id.to_string(),
                    question: in_.question,
                    all_options: in_.options.is_none(),
                    options: in_.options.unwrap_or_default(),
                    criteria: in_.criteria,
                    policy: pbconv::to_struct(&in_.policy),
                }),
            )
            .await)?;
        Ok(point_of(m.point))
    }

    async fn rule_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        in_: graphapi::RuleRequest,
    ) -> Result<DecisionPoint, Error> {
        let m = r(self
            .rpc
            .rule_decision(
                ctx,
                Request::new(pb::RuleDecisionRequest {
                    change_id: id.to_string(),
                    point: in_.point,
                    outcome: in_.outcome,
                    option: in_.option,
                    confidence: in_.confidence,
                    justification: in_.justification,
                    questions: in_.questions,
                    agent: !in_.human,
                }),
            )
            .await)?;
        Ok(point_of(m.point))
    }

    async fn answer_question(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        question: &str,
        answer: &str,
        process: &str,
        _by: &str,
    ) -> Result<DecisionPoint, Error> {
        let m = r(self
            .rpc
            .answer_question(
                ctx,
                Request::new(pb::AnswerQuestionRequest {
                    change_id: id.to_string(),
                    question: question.to_string(),
                    answer: answer.to_string(),
                    process: process.to_string(),
                }),
            )
            .await)?;
        Ok(point_of(m.point))
    }

    async fn ratify_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        point: &str,
        accept: bool,
        _by: &str,
        comment: &str,
    ) -> Result<DecisionPoint, Error> {
        let m = r(self
            .rpc
            .ratify_decision(
                ctx,
                Request::new(pb::RatifyDecisionRequest {
                    change_id: id.to_string(),
                    point: point.to_string(),
                    accept,
                    comment: comment.to_string(),
                }),
            )
            .await)?;
        Ok(point_of(m.point))
    }

    async fn decision_points(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<DecisionPoint>, Error> {
        let m = r(self
            .rpc
            .list_decision_points(
                ctx,
                Request::new(pb::ListDecisionPointsRequest {
                    change_id: id.to_string(),
                }),
            )
            .await)?;
        Ok(m.points
            .iter()
            .map(|d| pbconv::decision_point_from_pb(Some(d)))
            .collect())
    }

    async fn apply(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        baseline_name: &str,
    ) -> Result<Baseline, Error> {
        let m = r(self
            .rpc
            .apply_change(
                ctx,
                Request::new(pb::ApplyChangeRequest {
                    change_id: id.to_string(),
                    baseline_name: baseline_name.to_string(),
                }),
            )
            .await)?;
        Ok(baseline_of(m.baseline))
    }

    async fn branch(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Branch, Error> {
        let m = r(self
            .rpc
            .get_branch(
                ctx,
                Request::new(pb::GetBranchRequest {
                    namespace: namespace.to_string(),
                    name: name.to_string(),
                }),
            )
            .await)?;
        Ok(m.branch
            .as_ref()
            .map(pbconv::branch_from_pb)
            .unwrap_or_default())
    }

    async fn plan_merge(
        &self,
        ctx: &Ctx,
        namespace: &str,
        from: &str,
        into: &str,
    ) -> Result<graphapi::MergePlan, Error> {
        let m = r(self
            .rpc
            .plan_merge(
                ctx,
                Request::new(pb::PlanMergeRequest {
                    namespace: namespace.to_string(),
                    from: from.to_string(),
                    into: into.to_string(),
                }),
            )
            .await)?;
        Ok(pbconv::merge_plan_from_pb(&m.plan.unwrap_or_default()))
    }

    /// IntegrateChange (MergeChange over RPC).
    async fn integrate_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        resolutions: BTreeMap<NodeID, graphapi::Resolution>,
    ) -> Result<Change, Error> {
        let m = r(self
            .rpc
            .merge_change(
                ctx,
                Request::new(pb::MergeChangeRequest {
                    change_id: id.to_string(),
                    resolutions: pbconv::resolutions_to_pb(&resolutions),
                }),
            )
            .await)?;
        Ok(change_of(m.change))
    }

    /// RebaseState tells where a change stands against its parent or its target branch (ADR 0082, 0101).
    async fn rebase_state(&self, ctx: &Ctx, id: &ChangeID) -> Result<graphapi::RebaseState, Error> {
        let m = r(self
            .rpc
            .get_rebase_state(
                ctx,
                Request::new(pb::GetRebaseStateRequest {
                    change_id: id.to_string(),
                }),
            )
            .await)?;
        Ok(graphapi::RebaseState {
            parent: ChangeID::new(m.parent_id),
            branch: m.branch,
            onto: BaselineID::new(m.onto),
            behind: m
                .behind_impact_ids
                .into_iter()
                .map(ChangeImpactID::new)
                .collect(),
            conflicts: m
                .conflicts
                .into_iter()
                .map(|x| graphapi::ImpactConflicts {
                    impact: ChangeImpactID::new(x.change_impact_id),
                    key: x.key,
                    conflicts: x.conflicts,
                })
                .collect(),
        })
    }

    async fn baselines(&self, ctx: &Ctx, namespace: &str) -> Result<Vec<Baseline>, Error> {
        let m = r(self
            .rpc
            .list_baselines(
                ctx,
                Request::new(pb::ListBaselinesRequest {
                    namespace: namespace.to_string(),
                }),
            )
            .await)?;
        Ok(m.baselines.iter().map(pbconv::baseline_from_pb).collect())
    }

    /// AppendLog appends entries to the logs of their changes (graph.Graph.AppendLog).
    async fn append_log(&self, ctx: &Ctx, entries: Vec<LogEntry>) -> Result<(), Error> {
        r(self
            .rpc
            .append_log(
                ctx,
                Request::new(pb::AppendLogRequest {
                    entries: entries.iter().map(pbconv::log_entry_to_pb).collect(),
                }),
            )
            .await)
        .map(|_| ())
    }

    /// ChangeLog returns the entries of a change's log matching f, and the number of entries of each type matching f
    /// without its types (graph.Graph.ChangeLog).
    async fn change_log(
        &self,
        ctx: &Ctx,
        f: LogFilter,
    ) -> Result<(Vec<LogEntry>, BTreeMap<String, i64>), Error> {
        let flows = f
            .flows
            .unwrap_or_default()
            .into_iter()
            .map(|fl| {
                if fl.is_empty() {
                    domain::MAIN_FLOW.to_string()
                } else {
                    fl
                }
            })
            .collect();
        let m = r(self
            .rpc
            .list_change_log(
                ctx,
                Request::new(pb::ListChangeLogRequest {
                    change_id: f.change.to_string(),
                    types: f.types,
                    flows,
                    process_ids: f.processes,
                    execution: f.execution,
                    after_seq: f.after_seq,
                    limit: f.limit as i32,
                }),
            )
            .await)?;
        let counts = m
            .counts
            .iter()
            .map(|(t, n)| (t.clone(), i64::from(*n)))
            .collect();
        Ok((pbconv::log_entries_from_pb(&m.entries), counts))
    }

    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Baseline, Error> {
        let m = r(self
            .rpc
            .get_branch(
                ctx,
                Request::new(pb::GetBranchRequest {
                    namespace: namespace.to_string(),
                    name: name.to_string(),
                }),
            )
            .await)?;
        Ok(baseline_of(m.head))
    }

    async fn list_changes(
        &self,
        ctx: &Ctx,
        f: graphapi::ChangesFilter,
    ) -> Result<Vec<Change>, Error> {
        let m = r(self
            .rpc
            .list_changes(
                ctx,
                Request::new(pb::ListChangesRequest {
                    namespace: f.namespace,
                    owner_org: f.owner_org,
                    status: ids(&f.status),
                }),
            )
            .await)?;
        Ok(m.changes.iter().map(pbconv::change_from_pb).collect())
    }

    /// Structures returns the structures of the graph (ADR 0054).
    async fn structures(&self, ctx: &Ctx) -> Result<Structures, Error> {
        let m = r(self
            .rpc
            .get_structures(ctx, Request::new(pb::GetStructuresRequest {}))
            .await)?;
        Ok(pbconv::structures_from_pb(&m))
    }

    /// AdminOnlyType tells whether a node type is written by platform administrators only (ADR 0068); a graph
    /// that cannot be reached answers true (with the error), so that nothing slips through the gate.
    async fn admin_only_type(&self, ctx: &Ctx, typ: &str) -> Result<bool, Error> {
        let m = r(self
            .rpc
            .is_admin_only_type(
                ctx,
                Request::new(pb::IsAdminOnlyTypeRequest {
                    r#type: typ.to_string(),
                }),
            )
            .await)?;
        Ok(m.admin_only)
    }
}

// The client is the graph as its callers see it over RPC (graphapi::Port, the engine's GraphPort).
const _: fn(&Client) -> &dyn graphapi::Port = |c| c;
