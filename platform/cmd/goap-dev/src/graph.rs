//! The graph and what judges who may do what on it (Go's graph.go), with the late hooks the graph is built with.

use std::sync::{Arc, OnceLock, Weak};

use async_trait::async_trait;
use goap_access::{self as access, AdminFloorValidator, Authorizer, Directory};
use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, Blackboard, Change, FACET_CRITICALITY_POLICY, GateResult, GuardianRef,
    Lifecycle, Link, Node, Structures, Transition,
};
use goap_graph::{BlackboardFacet, EventSink, Graph, GraphEvent, TypeCatalog};
use goap_graphsvc as graphsvc;
use goap_journal::{StepDependencies, StepDeps};
use goap_registrysvc::Service as Registry;
use goap_typecat::Live;

use crate::app::{BoxError, Env};
use crate::local::Stores;

type GResult<T> = Result<T, goap_graphapi::Error>;

/// The directory of the organisation and the authorizer over it.
type Access = (Arc<Directory>, Arc<Authorizer>);

/// GraphPart is the graph and what judges who may do what on it.
pub(crate) struct GraphPart {
    pub g: Arc<Graph>,
    pub directory: Arc<Directory>,
    pub authorizer: Arc<Authorizer>,
}

/// build_graph opens the graph on its repository, compacts its old baselines in the background and wires the access
/// hooks. The registry hooks (types, landing gate, lifecycles, history, operations) are late ones (env.late), bound
/// by build_registry; the event sinks by build_platform.
pub(crate) fn build_graph(e: &Env, st: &Stores) -> Result<GraphPart, BoxError> {
    let mut built: Option<Result<Access, BoxError>> = None;
    let late = e.late.clone();
    let g = Arc::new_cyclic(|me: &Weak<Graph>| {
        let mut g = Graph::new(st.graph.clone());
        // the principal behind each event of the impact logs (ADR 0029)
        g.caller = Some(Arc::new(graphsvc::caller));
        // confidence, rounds and deadline settle the decision points (ADR 0067)
        g.decision_policy = Some(Arc::new(goap_decision::Policy));
        // a verifier is not the producer (ADR 0075)
        g.review_policy = Some(Arc::new(goap_verify::Policy));
        // the registry's hooks (build_registry) and the event sinks (build_platform), bound once they exist
        {
            let late = late.clone();
            g.set_types(move || late.types.get().map(|t| t.get() as Arc<dyn TypeCatalog>));
        }
        g.sub_change_validator = Some(late.clone());
        g.lifecycles = Some(late.clone());
        g.log_facets = graphsvc::log_facets(Some(late.clone()));
        g.observe(late.clone());
        let directory = Arc::new(Directory::new(Arc::new(InProcess(me.clone()))));
        let authorizer = match Authorizer::new(directory.clone()) {
            Ok(a) => Arc::new(a),
            Err(err) => {
                built = Some(Err(Box::new(err)));
                return g;
            }
        };
        let authz: Arc<dyn goap_authz::Authorizer> = authorizer.clone();
        g.authorizer = Some(graphsvc::transition_authorizer(Some(authz.clone())));
        g.change_authorizer = Some(graphsvc::change_transition_authorizer(Some(authz.clone())));
        g.project_move_gate = Some(graphsvc::project_move_gate(
            Some(authz.clone()),
            Some(directory.clone()),
        ));
        g.item_authorizer = Some(graphsvc::item_authorizer(
            Some(authz.clone()),
            Some(directory.criticality_resolver()),
        ));
        // who may perform each operation on a change: the roles of its methodology (ADR 0103)
        g.operation_authorizer = Some(graphsvc::operation_authorizer(
            Some(authz),
            Some(late.operation_roles()),
        ));
        // the oracle and the lifetime a level accepts (ADR 0075 §3)
        g.item_policy = Some(Arc::new(Criticality(goap_criticality::item_policy(Some(
            directory.criticality_resolver(),
        )))));
        let facet: BlackboardFacet = directory.criticality_facet();
        g.facets.insert(FACET_CRITICALITY_POLICY.into(), facet);
        g.validators = vec![
            Arc::new(AdminFloorValidator),
            Arc::new(goap_llmcfg::ProtectedAliasValidator),
        ];
        built = Some(Ok((directory, authorizer)));
        g
    });
    let (directory, authorizer) = built.expect("the hooks are built with the graph")?;
    // baselines written whole before they were stored as deltas are compacted, once, in the background (ADR 0032)
    {
        let (g, ctx) = (g.clone(), e.ctx.clone());
        tokio::spawn(async move {
            match g.compact_baselines(&ctx).await {
                Err(e) => tracing::error!(err = %e, "compact baselines"),
                Ok(n) if n > 0 => tracing::info!(rewritten = n, "baselines compacted"),
                Ok(_) => {}
            }
        });
    }
    Ok(GraphPart {
        g,
        directory,
        authorizer,
    })
}

/// The graph of this process as the directory reads it (Go hands `*graph.Graph` itself to `access.Directory`). The
/// graph holds the authorizers built over the directory, so the directory holds it weakly.
struct InProcess(Weak<Graph>);

impl InProcess {
    fn graph(&self) -> GResult<Arc<Graph>> {
        self.0
            .upgrade()
            .ok_or_else(|| goap_graphapi::Error::msg("the graph is gone"))
    }
}

#[async_trait]
impl goap_graphsnap::Graph for InProcess {
    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> GResult<Baseline> {
        self.graph()?.branch_head(ctx, namespace, name).await
    }

    async fn baseline_graph(&self, ctx: &Ctx, id: &BaselineID) -> GResult<(Vec<Node>, Vec<Link>)> {
        self.graph()?.baseline_graph(ctx, id).await
    }
}

#[async_trait]
impl access::Graph for InProcess {
    async fn structures(&self, _ctx: &Ctx) -> GResult<Structures> {
        Ok(self.graph()?.structures())
    }
}

/// The criticality policy of the items as the graph asks it (Go's `criticality.ItemPolicy` satisfies the graph's
/// interface structurally); its refusal keeps the text of the policy.
struct Criticality(goap_criticality::ItemPolicy);

#[async_trait]
impl goap_graph::ItemPolicy for Criticality {
    async fn check(
        &self,
        ctx: &Ctx,
        c: &goap_domain::Change,
        it: &goap_domain::ChangeItem,
    ) -> goap_graph::Result<()> {
        self.0
            .check(ctx, c, it)
            .await
            .map_err(|e| goap_graph::Error::msg(e.to_string()))
    }
}

/// Late holds the hooks of the graph bound after it is built (Go sets the fields of `*graph.Graph` as the parts come):
/// the methodology registry (lifecycles, landing gate, step dependencies, operation roles; ADR 0058, 0066, 0103,
/// 0109), the type catalogue (ADR 0012) and the event sinks (ADR 0026, 0053). Before it is bound, a hook does what
/// Go's unset field does: no registry, an untyped graph, no event.
#[derive(Default)]
pub(crate) struct Late {
    pub registry: OnceLock<Weak<Registry>>,
    pub types: OnceLock<Arc<Live>>,
    pub sink: OnceLock<Arc<dyn EventSink>>,
}

impl Late {
    fn registry(&self) -> GResult<Arc<Registry>> {
        self.registry
            .get()
            .and_then(Weak::upgrade)
            .ok_or_else(|| goap_graphapi::Error::msg("the registry is not started"))
    }

    /// The roles of a methodology that allow an operation (Go's `reg.OperationRoles`).
    fn operation_roles(self: &Arc<Self>) -> graphsvc::OperationRoles {
        let late = self.clone();
        Arc::new(move |ctx: &Ctx, methodology: &str, op: &str| {
            let late = late.clone();
            let (ctx, methodology, op) = (ctx.clone(), methodology.to_string(), op.to_string());
            Box::pin(async move {
                late.registry()?
                    .operation_roles(&ctx, &methodology, &op)
                    .await
                    .map_err(goap_registrysvc::Error::into_graph)
            })
        })
    }
}

#[async_trait]
impl goap_graph::ChangeLifecycles for Late {
    async fn methodology_version(&self, ctx: &Ctx, methodology: &str) -> GResult<String> {
        self.registry()?
            .methodology_version(ctx, methodology)
            .await
            .map_err(goap_registrysvc::Error::into_graph)
    }
    async fn lifecycle(&self, ctx: &Ctx, methodology: &str) -> GResult<Option<Lifecycle>> {
        self.registry()?
            .lifecycle(ctx, methodology)
            .await
            .map_err(goap_registrysvc::Error::into_graph)
    }
    async fn default_goal(&self, ctx: &Ctx, methodology: &str) -> GResult<String> {
        self.registry()?
            .default_goal(ctx, methodology)
            .await
            .map_err(goap_registrysvc::Error::into_graph)
    }
    async fn guard(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        expr: &str,
        transition: &str,
        decision: &str,
    ) -> GResult<bool> {
        self.registry()?
            .guard(ctx, bb, expr, transition, decision)
            .await
            .map_err(goap_registrysvc::Error::into_graph)
    }
    async fn gate(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        t: &Transition,
        decision: &str,
    ) -> GResult<GateResult> {
        self.registry()?
            .gate(ctx, bb, t, decision)
            .await
            .map_err(goap_registrysvc::Error::into_graph)
    }
    async fn merge(&self, ctx: &Ctx, c: &Change, bb: &Blackboard) -> GResult<(bool, bool)> {
        self.registry()?
            .merge(ctx, c, bb)
            .await
            .map_err(goap_registrysvc::Error::into_graph)
    }
    async fn guardians(
        &self,
        ctx: &Ctx,
        methodology: &str,
        project: &str,
    ) -> GResult<Vec<GuardianRef>> {
        self.registry()?
            .guardians(ctx, methodology, project)
            .await
            .map_err(goap_registrysvc::Error::into_graph)
    }
    async fn integrates(&self, ctx: &Ctx, integrator: &str, methodology: &str) -> GResult<bool> {
        self.registry()?
            .integrates(ctx, integrator, methodology)
            .await
            .map_err(goap_registrysvc::Error::into_graph)
    }
    async fn check_guardians(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        transition: &str,
        ancestors: &[Change],
    ) -> GResult<String> {
        self.registry()?
            .check_guardians(ctx, bb, transition, ancestors)
            .await
            .map_err(goap_registrysvc::Error::into_graph)
    }
}

#[async_trait]
impl goap_graph::SubChangeValidator for Late {
    async fn validate(&self, ctx: &Ctx, parent: &Change, child: &Change) -> GResult<()> {
        self.registry()?
            .sub_change_validator(ctx, parent, child)
            .await
            .map_err(goap_registrysvc::Error::into_graph)
    }
}

/// The history of a change, read by the conditions and guards the graph evaluates: its steps rely on one another as
/// their methodology says (ADR 0109; Go's `reg.Methodology(ctx, name)` then `StepDependencies()`, nil on an error).
#[async_trait]
impl StepDependencies for Late {
    async fn step_dependencies(&self, ctx: &Ctx, methodology: &str) -> Option<StepDeps> {
        let reg = self.registry().ok()?;
        let m = reg.methodology(ctx, methodology).await.ok()?;
        Some(m.step_dependencies())
    }
}

#[async_trait]
impl EventSink for Late {
    async fn publish(&self, ctx: &Ctx, subject: &str, ev: &GraphEvent) -> goap_graph::Result<()> {
        match self.sink.get() {
            Some(s) => s.publish(ctx, subject, ev).await,
            None => Ok(()),
        }
    }
}
