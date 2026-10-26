//! The hooks a graph offers the methodologies of its changes (ADR 0168): the graph knows no methodology. What a change
//! follows (its version, its main goal, its lifecycle, its guardian chain), the guard and the gate of each move of its
//! state, the guardian of its commits and of its chain, the cascade of its sub-changes, the roles of its operations and
//! the steps its history weighs are asked of a [`Guardian`] the composition plugs, a client of the registry's
//! `ChangeGuardianService` (Go's `graph.ChangeLifecycles`, `SubChangeValidator`, `OperationAuthorizer` and
//! `LogFacets`). The graph sends what it judges on: the blackboard of the change, read in the transaction of the move
//! or the commit (ADR 0140).
use std::collections::{BTreeMap, BTreeSet};

use chrono::{DateTime, Utc};
use serde::Deserialize;
use serde_json::{Map, Value, json};

use graph_catalog::Catalog;

use crate::{
    Result, access,
    change::{Change, GuardianRef},
    criticality,
    decision::DecisionPoint,
    dependency,
    flow::{self, Flows, MAIN_FLOW},
    model::{ChangeId, Node, NodeView},
    read::World,
    store::ReadTx,
    view::View,
    write::{decision, flow as wflow, whole},
};

/// The key of `Change.Data` naming the Activity a change is scoped to (`registrysvc.DataActivity`).
pub const DATA_ACTIVITY: &str = "activityRef";

/// The Activity a change is scoped to, empty when it is scoped to none (`registrysvc.ActivityOf`): a change scoped to
/// none is judged by the graph alone at its commit, its guardian asked nothing of it.
pub fn activity_of(c: &Change) -> &str {
    c.data
        .as_ref()
        .and_then(|d| d.get(DATA_ACTIVITY))
        .and_then(Value::as_str)
        .unwrap_or_default()
}

/// The steps each step of a methodology relies on (`Compiled.StepDependencies`).
pub type StepDeps = BTreeMap<String, BTreeSet<String>>;

/// What the criteria of a transition came to (`domain.GateResult`): the vetos that do not hold, the objectives not met
/// and the derogation covering each of those.
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct GateResult {
    pub vetoed: Vec<String>,
    pub unmet: Vec<String>,
    pub reserve: BTreeMap<String, String>,
}

impl GateResult {
    /// The objectives not met that no derogation covers (`GateResult.Uncovered`).
    pub fn uncovered(&self) -> Vec<&str> {
        self.unmet
            .iter()
            .filter(|u| !self.reserve.contains_key(*u))
            .map(String::as_str)
            .collect()
    }
}

/// What a graph asks of the methodologies of its changes. A methodology is named by a reference, `<name>@<version>`,
/// a bare name being its latest published version.
#[async_trait::async_trait]
pub trait Guardian: Send + Sync {
    /// The version a change of a methodology follows; empty when the registry does not know it.
    async fn methodology_version(&self, methodology: &str) -> Result<String>;
    /// The JSON of the lifecycle the changes of a methodology follow, gates merged in; none when it names none.
    async fn lifecycle(&self, methodology: &str) -> Result<Option<Value>>;
    /// The main goal the changes of a methodology start with.
    async fn default_goal(&self, methodology: &str) -> Result<String>;
    /// The guard of a transition of the lifecycle of a change.
    async fn guard(&self, board: &Board, expr: &str, transition: &str, decision: &str) -> Result<bool>;
    /// The vetos and objectives of a transition, given as its JSON.
    async fn gate(&self, board: &Board, transition: &Value, decision: &str) -> Result<GateResult>;
    /// The say of the methodology on a commit: `(decided, ok)`, undecided leaving it to the committable states.
    async fn commit(&self, change: &Change, board: &Board) -> Result<(bool, bool)>;
    /// The guardian chain of a new root change of a methodology in a project (its key).
    async fn guardians(&self, methodology: &str, project: &str) -> Result<Vec<GuardianRef>>;
    /// Whether the changes of a methodology (a reference) integrate the changes of another (a name).
    async fn integrates(&self, integrator: &str, methodology: &str) -> Result<bool>;
    /// The outer guardians of the chain of a change on a transition, or on its commit when none is named; the
    /// ancestors nearest first. The refusal, empty when they all agree.
    async fn check_guardians(&self, board: &Board, transition: &str, ancestors: &[Change]) -> Result<String>;
    /// The activity of a sub-change against its parent's: an invalid argument when it is not part of it.
    async fn check_sub_change(&self, parent: &Change, child: &Change) -> Result<()>;
    /// The roles of a methodology allowed an operation on its changes; none when the methodology restricts nothing.
    async fn operation_roles(&self, methodology: &str, operation: &str) -> Result<Option<Vec<String>>>;
    /// Which steps of a methodology rely on which; none for a methodology the registry cannot compile.
    async fn step_dependencies(&self, methodology: &str) -> Result<StepDeps>;
}

/// The blackboard of a change as the guardian judges it (Go's `Graph.Blackboard` with its facets): the change with its
/// impacts and the items its flow sees, the node views of the versions its impacts name and their neighbours, its
/// options, its decision points, the instant it is read at, and the facets the graph adds, each the JSON of its value
/// as Go writes it.
#[derive(Debug, Clone)]
pub struct Board {
    pub change: whole::WholeChange,
    /// the items of the change as the replay of its flows reads them, one per entry of `change.items`
    pub items: Vec<flow::Item>,
    pub flows: Flows,
    /// the flow the board is read on (empty: the main flow)
    pub flow: String,
    pub nodes: Vec<NodeView>,
    /// whether each node rests in a state that cannot commit
    pub not_committable: Vec<bool>,
    pub neighbors: Vec<Node>,
    pub decision_points: Vec<DecisionPoint>,
    pub at: DateTime<Utc>,
    pub facets: Map<String, Value>,
}

impl Board {
    /// The indexes of the items the flow of the board sees, each with whether it is of its chain (carried plain).
    pub fn seen_items(&self) -> Vec<(usize, bool)> {
        let chain: Vec<&str> = self.flows.chain(&self.flow).iter().map(|f| f.id.as_str()).collect();
        self.flows
            .view(&self.items, &self.flow)
            .into_iter()
            .map(|i| (i, chain.contains(&self.items[i].flow.as_str())))
            .collect()
    }
}

/// The blackboard of a change on a flow (none: its active option), at an instant, without its facets (`GetBlackboard`).
pub async fn board<T: ReadTx>(
    tx: &T,
    cat: &Catalog,
    id: &ChangeId,
    flow: Option<&str>,
    at: DateTime<Utc>,
) -> Result<Board> {
    let items = wflow::items_of(tx, id).await?;
    let flows = Flows::replay(&items);
    let flow = flows.resolve(flow.unwrap_or_default());
    let change = whole::whole_change_on(tx, cat, id, Some(&flow)).await?;
    let v = View {
        change: id.clone(),
        flow: if flow.is_empty() {
            MAIN_FLOW.into()
        } else {
            flow.clone()
        },
        ..Default::default()
    };
    let world = World::open(tx, &v).await?;
    let (nodes, neighbors) = world.blackboard().await?;
    let not_committable = nodes
        .iter()
        .map(|v| {
            !v.node.state.is_empty()
                && cat
                    .lifecycle(&v.node.typ)
                    .is_some_and(|lc| !lc.committable(&v.node.state))
        })
        .collect();
    let decision_points = decision::points_of(tx, id, at).await?;
    Ok(Board {
        change,
        items,
        flows,
        flow,
        nodes,
        not_committable,
        neighbors,
        decision_points,
        at,
        facets: Map::new(),
    })
}

/// The blackboard a guardian judges a change on: [`board`] with what the graph adds (Go's `FacetDependencies`,
/// `Graph.CriticalityOf` and `LogFacets`): the changes it depends on, what its organisation requires of its
/// criticality and the history of its runs, each as Go writes it.
pub async fn judged<T: ReadTx>(tx: &T, cat: &Catalog, c: &Change, at: DateTime<Utc>) -> Result<Board> {
    let mut b = board(tx, cat, &c.id, None, at).await?;
    let deps: Vec<Value> = dependency::dependencies_of(tx, c)
        .await?
        .iter()
        .map(|d| {
            json!({"id": d.id.as_str(), "title": d.title, "methodology": d.methodology, "status": d.status,
                "state": d.state, "parentId": d.parent_id.as_str(), "satisfied": d.satisfied, "reason": d.reason})
        })
        .collect();
    b.facets.insert("dependencies".into(), Value::Array(deps));
    let level = criticality::of(c.data.as_ref());
    let p = access::Snapshot::read(tx)
        .await?
        .criticality_policy(&c.owner_org.key, level);
    b.facets.insert(
        "criticalityPolicy".into(),
        json!({"oracles": p.oracles, "sampling": p.sampling, "signatoryRole": p.signatory_role,
            "maxDerogation": p.max_derogation.num_nanoseconds().unwrap_or(i64::MAX)}),
    );
    let history = crate::history::of(tx, c).await?;
    b.facets.insert(
        "history".into(),
        serde_json::to_value(history).map_err(crate::GraphError::internal)?,
    );
    Ok(b)
}
