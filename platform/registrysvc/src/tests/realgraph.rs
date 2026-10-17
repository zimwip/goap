//! The real graph under the registry in the tests (goap-graph, a dev-dependency as in Go, where the package imports
//! graph/graph only in its tests): `StoreGraph` for `goap_graph::Graph`, the registry as the graph's
//! `ChangeLifecycles` / `SubChangeValidator` (Go's `Service` satisfies them structurally), and [`Probe`], what the
//! tests read of a graph, implemented by both the real graph and the [`FakeGraph`].

use std::sync::{Arc, OnceLock, Weak};

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, Blackboard, Change, ChangeID, ChangeImpact, GateResult, GuardianRef,
    Lifecycle, Link, Node, Structures, Transition,
};
use goap_graphapi::{Commit, CommitResult, NewChange, NodeCreate};

type GResult<T> = std::result::Result<T, goap_graphapi::Error>;

use super::FakeGraph;
use crate::graphstore::StoreGraph;
use crate::service::Service;

#[async_trait]
impl StoreGraph for goap_graph::Graph {
    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> GResult<Baseline> {
        goap_graph::Graph::branch_head(self, ctx, namespace, name).await
    }
    async fn baseline_graph(&self, ctx: &Ctx, id: &BaselineID) -> GResult<(Vec<Node>, Vec<Link>)> {
        goap_graph::Graph::baseline_graph(self, ctx, id).await
    }
    async fn commit(&self, ctx: &Ctx, input: Commit) -> GResult<CommitResult> {
        Box::pin(goap_graph::Graph::commit(self, ctx, input)).await
    }
    async fn create_change(&self, ctx: &Ctx, input: NewChange) -> GResult<Change> {
        goap_graph::Graph::create_change(self, ctx, input).await
    }
    async fn impact_node_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: NodeCreate,
    ) -> GResult<ChangeImpact> {
        goap_graph::Graph::impact_node_create(self, ctx, id, input).await
    }
    async fn changes(&self, ctx: &Ctx) -> GResult<Vec<Change>> {
        goap_graph::Graph::changes(self, ctx).await
    }
    async fn structures(&self, ctx: &Ctx) -> GResult<Structures> {
        goap_graphapi::Port::structures(self, ctx).await
    }
    async fn suspect_links(&self, ctx: &Ctx, id: &BaselineID) -> Option<GResult<Vec<Link>>> {
        Some(goap_graph::Graph::suspect_links(self, ctx, id).await)
    }
}

/// What the tests read of the graph under a store: the head version of a node by its key.
#[async_trait]
pub(crate) trait Probe: StoreGraph {
    async fn find(&self, ns: &str, key: &str) -> Option<Node>;
    /// The nodes and links of the head of main of a namespace, suspect links included.
    async fn head_graph(&self, ns: &str) -> (Vec<Node>, Vec<Link>);
}

#[async_trait]
impl Probe for FakeGraph {
    async fn find(&self, ns: &str, key: &str) -> Option<Node> {
        self.node_by_key(ns, key)
    }
    async fn head_graph(&self, ns: &str) -> (Vec<Node>, Vec<Link>) {
        FakeGraph::head_graph(self, ns)
    }
}

#[async_trait]
impl Probe for goap_graph::Graph {
    async fn find(&self, ns: &str, key: &str) -> Option<Node> {
        goap_graph::Graph::node_by_key(self, &Ctx::background(), ns, key)
            .await
            .ok()
    }
    async fn head_graph(&self, ns: &str) -> (Vec<Node>, Vec<Link>) {
        let ctx = Ctx::background();
        let head = goap_graph::Graph::branch_head(self, &ctx, ns, goap_domain::MAIN_BRANCH)
            .await
            .expect("head");
        crate::graphstore::baseline_with_suspects(self, &ctx, &head.id)
            .await
            .expect("baseline")
    }
}

/// An untyped graph in memory (Go's `graph.New(graph.NewMemory())`).
pub(crate) fn real() -> Arc<goap_graph::Graph> {
    Arc::new(goap_graph::Graph::new(Arc::new(goap_graph::Memory::new())))
}

/// A graph in memory checking the built-in meta-domain (strict attributes and link types; Go's typedStore).
pub(crate) fn real_typed() -> Arc<goap_graph::Graph> {
    let mut g = goap_graph::Graph::new(Arc::new(goap_graph::Memory::new()));
    let cat: Arc<dyn goap_graph::TypeCatalog> = goap_typecat::builtin();
    g.set_types(move || Some(cat.clone()));
    Arc::new(g)
}

/// The registry as the graph's guardian, bound once the registry exists (the graph is built before the store over
/// it, and the registry over the store: Go sets `g.Lifecycles = reg` afterwards).
#[derive(Default)]
pub(crate) struct Late(OnceLock<Weak<Service>>);

impl Late {
    pub(crate) fn bind(&self, s: &Arc<Service>) {
        let _ = self.0.set(Arc::downgrade(s));
    }

    fn get(&self) -> GResult<Arc<Service>> {
        self.0
            .get()
            .and_then(Weak::upgrade)
            .ok_or_else(|| goap_graphapi::Error::msg("the registry is not bound"))
    }
}

#[async_trait]
impl goap_graph::ChangeLifecycles for Late {
    async fn methodology_version(&self, ctx: &Ctx, methodology: &str) -> GResult<String> {
        self.get()?
            .methodology_version(ctx, methodology)
            .await
            .map_err(crate::Error::into_graph)
    }
    async fn lifecycle(&self, ctx: &Ctx, methodology: &str) -> GResult<Option<Lifecycle>> {
        self.get()?
            .lifecycle(ctx, methodology)
            .await
            .map_err(crate::Error::into_graph)
    }
    async fn default_goal(&self, ctx: &Ctx, methodology: &str) -> GResult<String> {
        self.get()?
            .default_goal(ctx, methodology)
            .await
            .map_err(crate::Error::into_graph)
    }
    async fn guard(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        expr: &str,
        transition: &str,
        decision: &str,
    ) -> GResult<bool> {
        self.get()?
            .guard(ctx, bb, expr, transition, decision)
            .await
            .map_err(crate::Error::into_graph)
    }
    async fn gate(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        t: &Transition,
        decision: &str,
    ) -> GResult<GateResult> {
        self.get()?
            .gate(ctx, bb, t, decision)
            .await
            .map_err(crate::Error::into_graph)
    }
    async fn merge(&self, ctx: &Ctx, c: &Change, bb: &Blackboard) -> GResult<(bool, bool)> {
        self.get()?
            .merge(ctx, c, bb)
            .await
            .map_err(crate::Error::into_graph)
    }
    async fn guardians(
        &self,
        ctx: &Ctx,
        methodology: &str,
        project: &str,
    ) -> GResult<Vec<GuardianRef>> {
        self.get()?
            .guardians(ctx, methodology, project)
            .await
            .map_err(crate::Error::into_graph)
    }
    async fn integrates(&self, ctx: &Ctx, integrator: &str, methodology: &str) -> GResult<bool> {
        self.get()?
            .integrates(ctx, integrator, methodology)
            .await
            .map_err(crate::Error::into_graph)
    }
    async fn check_guardians(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        transition: &str,
        ancestors: &[Change],
    ) -> GResult<String> {
        self.get()?
            .check_guardians(ctx, bb, transition, ancestors)
            .await
            .map_err(crate::Error::into_graph)
    }
}

#[async_trait]
impl goap_graph::SubChangeValidator for Late {
    async fn validate(&self, ctx: &Ctx, parent: &Change, child: &Change) -> GResult<()> {
        self.get()?
            .sub_change_validator(ctx, parent, child)
            .await
            .map_err(crate::Error::into_graph)
    }
}
