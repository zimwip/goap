//! Port of `graph.go`: the [`Graph`] (the domain and change axes over a [`Repo`]), the [`TypeCatalog`] it judges the
//! nodes by, its seams, and the operations of `graph.go` itself: reading the domain axis, creating, reading, patching
//! changes and adding their items.
//!
//! Every nil-by-default seam of Go's `Graph` is an `Option<Arc<dyn …>>` field here; a Go function type is a trait of
//! one method. The blackboard reads of `graph.go` (`Blackboard`, `BlackboardIn`, `ChangeChecked`) need the drafts and
//! the validation of later slices: they are not ported yet (see PORTING.md).

use std::collections::BTreeMap;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_algo::Bound;
use goap_ctx::Ctx;
use goap_domain::{
    AttributeCheck, BRANCH_OPEN, Baseline, BaselineID, Blackboard, Branch, CHANGE_ABANDONED,
    CHANGE_ACTIVE, CHANGE_APPLIED, CHANGE_COMMITTED, CHANGE_DRAFT, Change, ChangeID, ChangeItem,
    ChangeStatus, DecisionPolicy, FLOW_OPEN, FacetValue, GateResult, GuardianRef, HeaderEdit,
    HeaderValue, INTENT_DERIVE, ITEM_PROPOSED, ItemID, ItemPermission, Lifecycle, Link, LinkID,
    MAIN_BRANCH, Node, NodeID, NodeRef, NodeView, REVIEW_STATUS_SUBMITTED, RequiredLink,
    ReviewPolicy, SearchProperty, Structure, Structures, TYPE_DECISION_POINT, TYPE_FLOW,
    TYPE_REVIEW, TYPE_TRANSITION, Transition, branch_of, change_branch_origin, item_permission_of,
    methodology_ref, namespace_of, review_record_of, valid_option_intent,
};
use serde_json::Value;

use crate::api::{ChangePatch, ChangesFilter, NewChange, NodeValidator};
use crate::changebranch::{change_branch_name, own_branch};
use crate::changelog::put_item;
use crate::events::EventSink;
use crate::merge::{branch_head, branch_of_tx};
use crate::option::{BlackboardFacet, LogFacet};
use crate::repo::{Repo, Tx};
use crate::state::StateCache;
use crate::{Error, ErrorKind, Result};

/// TypeCatalog is what the graph needs of the type catalogue (implemented by `goap_typecat::Catalog`). The types
/// are qualified references ("alm@Requirement"); an unknown type has no lifecycle, validator or search declaration.
pub trait TypeCatalog: Send + Sync {
    fn lifecycle(&self, typ: &str) -> Option<&Lifecycle>;
    fn validators(&self, typ: &str) -> &[Bound];
    /// Attributes are the attributes of a node type, to check the values of its nodes.
    fn attribute_checks(&self, typ: &str) -> Vec<AttributeCheck>;
    /// LinkAttributeChecks are the attributes of a link type, to check the properties of its links.
    fn link_attribute_checks(&self, typ: &str) -> Vec<AttributeCheck>;
    /// LinkAttributeValidators are the property validators plugged on the attributes of a link type (each bound to
    /// its attribute), LinkTypeValidators its link validators, both in call order.
    fn link_attribute_validators(&self, typ: &str) -> &[Bound];
    fn link_type_validators(&self, typ: &str) -> &[Bound];
    /// AttributeNames are the names of the attributes of a node type, ancestors included, and whether the type is
    /// open (`additionalProperties: true`, or unknown to the catalogue): the properties of a node of a closed type are
    /// attributes only.
    fn attribute_names(&self, typ: &str) -> (Vec<String>, bool);
    fn search(&self, typ: &str) -> &[SearchProperty];
    /// CheckNode is the existence rule of a node of namespace ns, CheckLink of a link between two node types; the
    /// error is the catalogue's message.
    fn check_node(&self, ns: &str, typ: &str) -> std::result::Result<(), String>;
    fn check_link(&self, typ: &str, from: &str, to: &str) -> std::result::Result<(), String>;
    /// Structure is the hierarchy a domain tags (ADR 0054: the organisation, the project); IsA reports a type or a
    /// subtype of base.
    fn structure(&self, kind: &str) -> Option<&Structure>;
    fn is_a(&self, typ: &str, base: &str) -> bool;
    /// Structures is the structures declared with the types belonging to each.
    fn structures(&self) -> Structures;
    /// Requires are the links a node of a type must carry (`requires:` on a node type, ADR 0065).
    fn requires(&self, typ: &str) -> &[RequiredLink];
    /// AdminOnly reports a node type written by platform administrators only (`adminOnly:` on a node type, ADR
    /// 0068).
    fn admin_only(&self, typ: &str) -> bool;
    /// Composes reports a composition link type (`compose: true`): its target is a part of its source (ADR 0077).
    fn composes(&self, link_type: &str) -> bool;
    /// ItemAttributeChecks are the attributes a type of change item declares for the data of its items, None when no
    /// domain declares the type (ADR 0128); ItemTypes lists the declared types.
    fn item_attribute_checks(&self, item_type: &str) -> Option<Vec<AttributeCheck>>;
    fn item_types(&self) -> Vec<&goap_typecat::ItemType>;
}

impl TypeCatalog for goap_typecat::Catalog {
    fn lifecycle(&self, typ: &str) -> Option<&Lifecycle> {
        goap_typecat::Catalog::lifecycle(self, typ)
    }
    fn validators(&self, typ: &str) -> &[Bound] {
        goap_typecat::Catalog::validators(self, typ)
    }
    fn attribute_checks(&self, typ: &str) -> Vec<AttributeCheck> {
        goap_typecat::Catalog::attribute_checks(self, typ)
    }
    fn link_attribute_checks(&self, typ: &str) -> Vec<AttributeCheck> {
        goap_typecat::Catalog::link_attribute_checks(self, typ)
    }
    fn link_attribute_validators(&self, typ: &str) -> &[Bound] {
        goap_typecat::Catalog::link_attribute_validators(self, typ)
    }
    fn link_type_validators(&self, typ: &str) -> &[Bound] {
        goap_typecat::Catalog::link_type_validators(self, typ)
    }
    fn attribute_names(&self, typ: &str) -> (Vec<String>, bool) {
        goap_typecat::Catalog::attribute_names(self, typ)
    }
    fn search(&self, typ: &str) -> &[SearchProperty] {
        goap_typecat::Catalog::search(self, typ)
    }
    fn check_node(&self, ns: &str, typ: &str) -> std::result::Result<(), String> {
        goap_typecat::Catalog::check_node(self, ns, typ).map_err(|e| e.to_string())
    }
    fn check_link(&self, typ: &str, from: &str, to: &str) -> std::result::Result<(), String> {
        goap_typecat::Catalog::check_link(self, typ, from, to).map_err(|e| e.to_string())
    }
    fn structure(&self, kind: &str) -> Option<&Structure> {
        goap_typecat::Catalog::structure(self, kind)
    }
    fn is_a(&self, typ: &str, base: &str) -> bool {
        goap_typecat::Catalog::is_a(self, typ, base)
    }
    fn structures(&self) -> Structures {
        goap_typecat::Catalog::structures(self)
    }
    fn requires(&self, typ: &str) -> &[RequiredLink] {
        goap_typecat::Catalog::requires(self, typ)
    }
    fn admin_only(&self, typ: &str) -> bool {
        goap_typecat::Catalog::admin_only(self, typ)
    }
    fn composes(&self, link_type: &str) -> bool {
        goap_typecat::Catalog::composes(self, link_type)
    }
    fn item_attribute_checks(&self, item_type: &str) -> Option<Vec<AttributeCheck>> {
        goap_typecat::Catalog::item_attribute_checks(self, item_type)
    }
    fn item_types(&self) -> Vec<&goap_typecat::ItemType> {
        goap_typecat::Catalog::item_types(self)
    }
}

// ---- seams (Go: function types and interfaces of graph.go, lifecycle.go, transition.go, move.go, operation.go) ----

/// TransitionAuthorizer decides whether the caller may take a transition on a node (lifecycle.go). Unset allows every
/// transition. n.state is the state it leaves.
#[async_trait]
pub trait TransitionAuthorizer: Send + Sync {
    async fn authorize(&self, ctx: &Ctx, n: &Node, t: &Transition) -> Result<()>;
}

/// ChangeTransitionAuthorizer decides whether the caller may take a transition of the lifecycle of a change
/// (transition.go). Unset allows every transition.
#[async_trait]
pub trait ChangeTransitionAuthorizer: Send + Sync {
    async fn authorize(&self, ctx: &Ctx, c: &Change, t: &Transition) -> Result<()>;
}

/// SubChangeValidator is asked by CreateChange about a sub-change before it is stored, with its parent as stored and
/// the change as it is about to be (data, methodology, ...): an error refuses it (an invalid one). Like the guardian
/// of a merge (ChangeLifecycles::merge) it runs outside any transaction and may read the graph.
#[async_trait]
pub trait SubChangeValidator: Send + Sync {
    async fn validate(&self, ctx: &Ctx, parent: &Change, child: &Change) -> Result<()>;
}

/// ItemAuthorizer judges the write of an item whose kind asks a permission of its writer: the permission it asks,
/// the change it is written on and the item. An error refuses it.
#[async_trait]
pub trait ItemAuthorizer: Send + Sync {
    async fn authorize(
        &self,
        ctx: &Ctx,
        c: &Change,
        it: &ChangeItem,
        p: &ItemPermission,
    ) -> Result<()>;
}

/// ItemPolicy judges any item about to be written on a change; an error refuses it.
#[async_trait]
pub trait ItemPolicy: Send + Sync {
    async fn check(&self, ctx: &Ctx, c: &Change, it: &ChangeItem) -> Result<()>;
}

/// OperationAuthorizer decides whether the caller (in ctx) may perform an operation of the catalogue
/// (goap-operations, ADR 0103) on a change: an error refuses it, typically a forbidden one (operation.go).
#[async_trait]
pub trait OperationAuthorizer: Send + Sync {
    async fn authorize(&self, ctx: &Ctx, c: &Change, op: &str) -> Result<()>;
}

/// ProjectMoveGate is asked before a change moves to another project (ADR 0091), outside any transaction (it reads
/// the graph): family is the change and its open sub-changes, each with the project it is in, and to the project they
/// move to (move.go). Unset: no check beyond the graph's own rules.
#[async_trait]
pub trait ProjectMoveGate: Send + Sync {
    async fn check(&self, ctx: &Ctx, family: &[Change], to: &str) -> Result<()>;
}

/// ChangeLifecycles is what the graph needs of the registry to run the lifecycle of a change: the methodologies are
/// not known to the graph (transition.go). The methodology every method takes is a name or a reference at a version
/// (`methodology_ref`, ADR 0113): a change reads the version it follows (`Change::methodology_ref`), a bare name the
/// latest published one.
#[async_trait]
pub trait ChangeLifecycles: Send + Sync {
    /// MethodologyVersion resolves the version a new change of a methodology follows (ADR 0113); "" when the registry
    /// does not know the methodology.
    async fn methodology_version(&self, ctx: &Ctx, methodology: &str) -> Result<String>;
    /// Lifecycle returns the lifecycle the changes of a methodology follow; None when it names none.
    async fn lifecycle(&self, ctx: &Ctx, methodology: &str) -> Result<Option<Lifecycle>>;
    /// DefaultGoal returns the main goal the changes of a methodology start with (ADR 0096); "" when the
    /// methodology is unknown or has none.
    async fn default_goal(&self, ctx: &Ctx, methodology: &str) -> Result<String>;
    /// Guard evaluates the guard of a transition for the change on bb.
    async fn guard(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        expr: &str,
        transition: &str,
        decision: &str,
    ) -> Result<bool>;
    /// Gate evaluates the vetos and the objectives of a transition in the same environment (ADR 0075 §3).
    async fn gate(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        t: &Transition,
        decision: &str,
    ) -> Result<GateResult>;
    /// Merge is the say of the methodology on the merge of a change (ADR 0066, 0102): (decided, ok).
    async fn merge(&self, ctx: &Ctx, c: &Change, bb: &Blackboard) -> Result<(bool, bool)>;
    /// Guardians is the guardian chain of a new root change of a methodology in a project (ADR 0106).
    async fn guardians(
        &self,
        ctx: &Ctx,
        methodology: &str,
        project: &str,
    ) -> Result<Vec<GuardianRef>>;
    /// Integrates reports whether the changes of the methodology integrator integrate the changes of methodology.
    async fn integrates(&self, ctx: &Ctx, integrator: &str, methodology: &str) -> Result<bool>;
    /// CheckGuardians asks the outer guardians of the chain of a change, innermost first, about a transition, or
    /// about its merge when transition is "": the refusal, "" when they all agree. ancestors are the parent changes,
    /// nearest first.
    async fn check_guardians(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        transition: &str,
        ancestors: &[Change],
    ) -> Result<String>;
}

/// The function giving the type catalogue in force.
pub type TypesFn = Arc<dyn Fn() -> Option<Arc<dyn TypeCatalog>> + Send + Sync>;
/// The function naming the principal behind an operation.
pub type CallerFn = Arc<dyn Fn(&Ctx) -> String + Send + Sync>;

/// Graph exposes the domain and change axes.
pub struct Graph {
    pub(crate) repo: Arc<dyn Repo>,
    pub(crate) now: Arc<dyn Fn() -> DateTime<Utc> + Send + Sync>,
    pub(crate) new_id: Arc<dyn Fn() -> String + Send + Sync>,
    /// sink receives the events of what each transaction wrote once it committed (Observe).
    pub(crate) sink: Option<Arc<dyn EventSink>>,
    /// Authorizer, when set, is asked before every lifecycle transition.
    pub authorizer: Option<Arc<dyn TransitionAuthorizer>>,
    /// Types returns the type catalogue in force (ADR 0012 §2, goap-typecat): the graph judges the nodes by it and
    /// refuses the ones whose type or link type it does not resolve. Unset: an untyped graph (tests, tools).
    pub types: Option<TypesFn>,
    /// Caller names the principal behind an operation, recorded on the events of the change impacts (ADR 0029).
    pub caller: Option<CallerFn>,
    /// Validators are the NodeValidator plugins checked once per Apply (ADR 0048), keyed by the node types they
    /// declare interest in via types().
    pub validators: Vec<Arc<dyn NodeValidator>>,
    /// SubChangeValidator, when set, is asked by CreateChange about a sub-change before it is stored.
    pub sub_change_validator: Option<Arc<dyn SubChangeValidator>>,
    /// Facets are the providers of the facets of a blackboard beyond the built-in ones, by name.
    pub facets: BTreeMap<String, BlackboardFacet>,
    /// LogFacets are the facets of a blackboard folded from the log of the change (ADR 0109), by name.
    pub log_facets: BTreeMap<String, LogFacet>,
    /// DecisionPolicy is the rule of the decision points of the changes (ADR 0067). Unset: the default policy.
    pub decision_policy: Option<Arc<dyn DecisionPolicy>>,
    /// ReviewPolicy is the rule of who may review a change impact (ADR 0075). Unset: anyone allowed to review may.
    pub review_policy: Option<Arc<dyn ReviewPolicy>>,
    /// ItemAuthorizer, when set, is asked before AddItems stores an item whose kind asks a permission of its writer.
    pub item_authorizer: Option<Arc<dyn ItemAuthorizer>>,
    /// ItemPolicy, when set, is asked about every item AddItems is about to store (ADR 0075 §3).
    pub item_policy: Option<Arc<dyn ItemPolicy>>,
    /// Lifecycles resolves the lifecycle of the changes of a methodology (ADR 0058). Unset: no change follows one.
    pub lifecycles: Option<Arc<dyn ChangeLifecycles>>,
    /// ChangeAuthorizer, when set, is asked before every transition of the lifecycle of a change (ADR 0058).
    pub change_authorizer: Option<Arc<dyn ChangeTransitionAuthorizer>>,
    /// OperationAuthorizer, when set, is asked before every operation of the catalogue a caller performs on a change
    /// (ADR 0103). Unset: every operation is allowed.
    pub operation_authorizer: Option<Arc<dyn OperationAuthorizer>>,
    /// ProjectMoveGate, when set, is asked before a change moves to another project (ADR 0091).
    pub project_move_gate: Option<Arc<dyn ProjectMoveGate>>,
    /// MaterializeEvery is how many baselines of a chain pass between two that are materialised (ADR 0056). 0:
    /// DEFAULT_MATERIALIZE_EVERY.
    pub materialize_every: usize,
    /// DraftCacheSize is how many changes keep their folded drafts in memory (ADR 0079 §2). 0: the default (the draft
    /// cache is a later slice).
    pub draft_cache_size: usize,
    /// booted is set once the roots of the structures are known to exist (Bootstrap).
    pub(crate) booted: AtomicBool,
    /// states caches the states computed for baselines kept as a header only.
    pub(crate) states: StateCache,
}

impl Graph {
    /// New returns a Graph backed by repo. Every transaction goes through the guard of the graph (ADR 0054),
    /// whatever the storage: [`Graph::begin`].
    pub fn new(repo: Arc<dyn Repo>) -> Graph {
        Graph {
            repo,
            now: Arc::new(Utc::now),
            new_id: Arc::new(|| uuid::Uuid::new_v4().to_string()),
            sink: None,
            authorizer: None,
            types: None,
            caller: None,
            validators: Vec::new(),
            sub_change_validator: None,
            facets: BTreeMap::new(),
            log_facets: BTreeMap::new(),
            decision_policy: None,
            review_policy: None,
            item_authorizer: None,
            item_policy: None,
            lifecycles: None,
            change_authorizer: None,
            operation_authorizer: None,
            project_move_gate: None,
            materialize_every: 0,
            draft_cache_size: 0,
            booted: AtomicBool::new(false),
            states: StateCache::default(),
        }
    }

    /// The repository under the graph (a test reaches its store through `as_any`).
    pub fn repo(&self) -> &Arc<dyn Repo> {
        &self.repo
    }

    /// Sets the clock of the graph (tests).
    pub fn set_now(&mut self, now: impl Fn() -> DateTime<Utc> + Send + Sync + 'static) {
        self.now = Arc::new(now);
    }

    /// Sets the id generator of the graph (tests).
    pub fn set_new_id(&mut self, f: impl Fn() -> String + Send + Sync + 'static) {
        self.new_id = Arc::new(f);
    }

    /// Sets the function giving the type catalogue in force.
    pub fn set_types(
        &mut self,
        f: impl Fn() -> Option<Arc<dyn TypeCatalog>> + Send + Sync + 'static,
    ) {
        self.types = Some(Arc::new(f));
    }

    /// The current time of the graph.
    pub(crate) fn now(&self) -> DateTime<Utc> {
        (self.now)()
    }

    /// A new id.
    pub(crate) fn new_id(&self) -> String {
        (self.new_id)()
    }

    /// catalog returns the type catalogue in force (None: an untyped graph, or a catalogue not loaded).
    pub(crate) fn catalog(&self) -> Option<Arc<dyn TypeCatalog>> {
        self.types.as_ref().and_then(|f| f())
    }

    // ---- Domain axis --------------------------------------------------------

    /// Node returns a node version (latest when r.version == 0).
    pub async fn node(&self, ctx: &Ctx, r: &NodeRef) -> Result<Node> {
        let mut tx = self.begin(ctx).await?;
        let n = tx.node(ctx, r).await?;
        tx.commit(ctx).await?;
        Ok(n)
    }

    /// NodeByKey returns the latest version of the node with the given key in a namespace.
    pub async fn node_by_key(&self, ctx: &Ctx, namespace: &str, key: &str) -> Result<Node> {
        let mut tx = self.begin(ctx).await?;
        let n = tx.node_by_key(ctx, namespace, key).await?;
        tx.commit(ctx).await?;
        Ok(n)
    }

    /// NodesOfType returns the latest nodes of a type in a namespace, live on main (existence checks, e.g. ADR 0040's
    /// first-user bootstrap): what the last change applied on main wrote, read from the versions themselves.
    pub async fn nodes_of_type(&self, ctx: &Ctx, namespace: &str, typ: &str) -> Result<Vec<Node>> {
        let mut tx = self.begin(ctx).await?;
        let all = tx.latest_nodes(ctx, namespace, MAIN_BRANCH).await?;
        tx.commit(ctx).await?;
        Ok(all
            .into_iter()
            .filter(|n| n.r#type == typ && !n.deleted)
            .collect())
    }

    /// OutLinksOf returns the outgoing links of a node version.
    pub async fn out_links_of(&self, ctx: &Ctx, r: &NodeRef) -> Result<Vec<Link>> {
        let mut tx = self.begin(ctx).await?;
        let ls = tx.out_links(ctx, r).await?;
        tx.commit(ctx).await?;
        Ok(ls)
    }

    /// LinkByID reads one link.
    pub async fn link_by_id(&self, ctx: &Ctx, id: &LinkID) -> Result<Link> {
        let mut tx = self.begin(ctx).await?;
        let l = tx.link(ctx, id).await?;
        tx.commit(ctx).await?;
        Ok(l)
    }

    /// View hydrates a node version with its neighbourhood.
    pub async fn view(&self, ctx: &Ctx, r: &NodeRef) -> Result<NodeView> {
        let mut tx = self.begin(ctx).await?;
        let v = view(ctx, &mut tx, r).await?;
        tx.commit(ctx).await?;
        Ok(v)
    }

    /// Baseline returns a baseline.
    pub async fn baseline(&self, ctx: &Ctx, id: &BaselineID) -> Result<Baseline> {
        let mut tx = self.begin(ctx).await?;
        let b = tx.baseline(ctx, id).await?;
        tx.commit(ctx).await?;
        Ok(b)
    }

    /// Baselines lists the baselines of a namespace, by creation date.
    pub async fn baselines(&self, ctx: &Ctx, namespace: &str) -> Result<Vec<Baseline>> {
        let mut tx = self.begin(ctx).await?;
        let bs = tx.baselines(ctx, namespace).await?;
        tx.commit(ctx).await?;
        Ok(bs)
    }

    /// BaselineGraph returns the nodes and links of a baseline.
    pub async fn baseline_graph(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>)> {
        let mut tx = self.begin(ctx).await?;
        let out = baseline_graph_tx(ctx, &mut tx, id).await?;
        tx.commit(ctx).await?;
        Ok(out)
    }

    /// SuspectLinks returns the links whose source is in the baseline but whose target node is in the baseline at
    /// another version: the target changed since the link was asserted.
    pub async fn suspect_links(&self, ctx: &Ctx, id: &BaselineID) -> Result<Vec<Link>> {
        let mut tx = self.begin(ctx).await?;
        let b = tx.baseline(ctx, id).await?;
        let mut out = Vec::new();
        for (nid, v) in &b.nodes {
            let links = tx
                .out_links(
                    ctx,
                    &NodeRef {
                        id: nid.clone(),
                        version: *v,
                    },
                )
                .await?;
            for l in links {
                if b.nodes.get(&l.to.id).is_some_and(|tv| *tv != l.to.version) {
                    out.push(l);
                }
            }
        }
        tx.commit(ctx).await?;
        Ok(out)
    }

    // ---- Change axis --------------------------------------------------------

    /// CreateChange opens a change on a reference baseline. A sub-change (parent_id) belongs to the namespace of its
    /// parent, is forked from the head of its parent's branch and merges into it (ADR 0103).
    pub async fn create_change(&self, ctx: &Ctx, mut input: NewChange) -> Result<Change> {
        self.bootstrap(ctx)
            .await
            .map_err(|e| e.context("bootstrap"))?;
        // a change whose methodology names a lifecycle starts in its initial state (ADR 0058); the registry is asked
        // before the transaction, it reads the graph
        let (mut lifecycle, mut initial) = (String::new(), String::new());
        let mut goal = input.goal.clone();
        // the version of the methodology the change follows (ADR 0113): a sub-change stays on its parent's, a new
        // change takes the latest published one; it keeps it while it runs
        let mut version = input.methodology_version.clone();
        if version.is_empty()
            && !input.methodology.is_empty()
            && !input.parent_id.is_empty()
            && let Ok(parent) = self.change(ctx, &input.parent_id).await
            && parent.methodology == input.methodology
        {
            version = parent.methodology_version;
        }
        if let Some(lc) = &self.lifecycles
            && !input.methodology.is_empty()
        {
            version = lc
                .methodology_version(ctx, &methodology_ref(&input.methodology, &version))
                .await?;
        }
        let r = methodology_ref(&input.methodology, &version);
        if let Some(lcs) = &self.lifecycles
            && !input.methodology.is_empty()
        {
            if goal.is_empty() {
                goal = lcs.default_goal(ctx, &r).await?;
            }
            if let Some(lc) = lcs.lifecycle(ctx, &r).await? {
                lifecycle = lc.name.clone();
                initial = lc.initial.clone();
            }
        }
        // the guardian chain (ADR 0106) is computed once, here, and saved on the change
        let guardians = self.guardian_chain(ctx, &input, &version).await?;
        // the validator of sub-changes is asked before the transaction too, it reads the graph
        if let Some(v) = &self.sub_change_validator
            && !input.parent_id.is_empty()
        {
            let parent = self.change(ctx, &input.parent_id).await?;
            let child = Change {
                title: input.title.clone(),
                intent: input.intent.clone(),
                methodology: input.methodology.clone(),
                methodology_version: version.clone(),
                namespace: namespace_of(&input.namespace).to_string(),
                parent_id: input.parent_id.clone(),
                owner_org: input.owner_org.clone(),
                project_id: input.project_id.clone(),
                data: input.data.clone(),
                ..Default::default()
            };
            v.validate(ctx, &parent, &child).await?;
        }
        let mut tx = self.begin(ctx).await?;
        let mut c = Change {
            id: ChangeID::new(self.new_id()),
            title: input.title.clone(),
            intent: input.intent.clone(),
            methodology: input.methodology.clone(),
            methodology_version: version,
            namespace: namespace_of(&input.namespace).to_string(),
            status: CHANGE_DRAFT,
            baseline_id: input.baseline_id.clone(),
            branch: branch_of(&input.branch).to_string(),
            data: input.data.clone(),
            created_at: self.now(),
            parent_id: input.parent_id.clone(),
            owner_org: input.owner_org.clone(),
            project_id: input.project_id.clone(),
            lifecycle,
            state: initial,
            goal,
            guardians,
            ..Default::default()
        };
        self.prepare_sub_change(ctx, &mut tx, &mut c, &mut input)
            .await?;
        // a change is held by a unit and acts in a project (ADR 0054, 0091), never left empty: a sub-change inherits
        // its parent's (prepare_sub_change), a change naming no unit is held by the root unit, and a change naming no
        // project is refused.
        self.scope_change(ctx, &mut tx, &mut c).await?;
        // a change naming no starting state starts from the head of its branch: the state the last change left, or
        // the empty state while the namespace has none (ADR 0056)
        if c.baseline_id.is_empty() {
            let head = branch_head(ctx, &mut tx, &c.namespace, &c.branch).await?;
            c.baseline_id = head.id;
        }
        let fork = tx.baseline(ctx, &c.baseline_id).await?;
        if !fork.id.is_empty() && fork.namespace != c.namespace {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "change acts on namespace {} but its reference baseline {} is of namespace {}",
                    c.namespace, c.baseline_id, fork.namespace
                ),
            ));
        }
        let b = branch_of_tx(ctx, &mut tx, &c.namespace, &c.branch).await?;
        if b.status != BRANCH_OPEN {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("branch {} is {}", b.name, b.status),
            ));
        }
        if input.own_branch {
            if !valid_option_intent(&input.branch_intent) {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!("unknown branch intent {:?}", input.branch_intent.as_str()),
                ));
            }
            let intent = if input.branch_intent.is_empty() {
                INTENT_DERIVE
            } else {
                input.branch_intent.clone()
            };
            let own = Branch {
                name: change_branch_name(&c.id),
                namespace: c.namespace.clone(),
                parent: b.name.clone(),
                fork_baseline: fork.id.clone(),
                head: fork.id.clone(),
                origin: change_branch_origin(&c.id),
                intent,
                status: BRANCH_OPEN.to_string(),
                created_at: self.now(),
                ..Default::default()
            };
            tx.put_branch(ctx, own.clone()).await?;
            c.branch = own.name;
        }
        tx.put_change(ctx, c.clone()).await?;
        tx.commit(ctx).await?;
        Ok(c)
    }

    /// Change returns a change with its items.
    pub async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        tx.commit(ctx).await?;
        Ok(c)
    }

    /// Changes lists changes.
    pub async fn changes(&self, ctx: &Ctx) -> Result<Vec<Change>> {
        let mut tx = self.begin(ctx).await?;
        let cs = tx.changes(ctx).await?;
        tx.commit(ctx).await?;
        Ok(cs)
    }

    /// ListChanges lists the changes matching a filter, in no particular order (the caller sorts and caps).
    pub async fn list_changes(&self, ctx: &Ctx, f: &ChangesFilter) -> Result<Vec<Change>> {
        let cs = self.changes(ctx).await?;
        Ok(cs.into_iter().filter(|c| f.matches(c)).collect())
    }

    /// UpdateChange patches a change header.
    pub async fn update_change(&self, ctx: &Ctx, id: &ChangeID, p: &ChangePatch) -> Result<Change> {
        let mut tx = self.change_tx(ctx, id).await?;
        let mut c = tx.change(ctx, id).await?;
        let mut edit = HeaderEdit::default();
        let text = |s: &str| Value::String(s.to_string());
        if let Some(t) = &p.title
            && *t != c.title
        {
            edit.fields.insert(
                "title".into(),
                HeaderValue {
                    from: text(&c.title),
                    to: text(t),
                },
            );
            c.title = t.clone();
        }
        if let Some(t) = &p.intent
            && *t != c.intent
        {
            edit.fields.insert(
                "intent".into(),
                HeaderValue {
                    from: text(&c.intent),
                    to: text(t),
                },
            );
            c.intent = t.clone();
        }
        if let Some(t) = &p.goal
            && *t != c.goal
        {
            edit.fields.insert(
                "goal".into(),
                HeaderValue {
                    from: text(&c.goal),
                    to: text(t),
                },
            );
            c.goal = t.clone();
        }
        if let Some(s) = &p.status
            && *s != c.status
        {
            if !valid_status_move(&c.status, s) {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("change {id} cannot go from {} to {s}", c.status),
                ));
            }
            edit.fields.insert(
                "status".into(),
                HeaderValue {
                    from: text(&c.status),
                    to: text(s),
                },
            );
            c.status = s.clone();
            if c.status == CHANGE_ABANDONED {
                self.abandon_sub_changes(ctx, &mut tx, &c.id).await?;
                if let Some(mut own) = own_branch(ctx, &mut tx, &c).await? {
                    own.status = goap_domain::BRANCH_ABANDONED.to_string();
                    tx.put_branch(ctx, own).await?;
                }
            }
        }
        if !p.data.is_empty() {
            for (k, v) in &p.data {
                let old = c.data.get(k);
                if old != Some(v) {
                    edit.fields.insert(
                        format!("data.{k}"),
                        HeaderValue {
                            from: old.cloned().unwrap_or(Value::Null),
                            to: v.clone(),
                        },
                    );
                }
            }
            for (k, v) in &p.data {
                c.data.insert(k.clone(), v.clone());
            }
        }
        if !edit.fields.is_empty() {
            let e = goap_domain::header_entry(
                &self.new_id(),
                &c.id,
                &self.caller_of(ctx),
                self.now(),
                &edit,
            )?;
            tx.append_log(ctx, e).await?;
        }
        tx.put_change(ctx, c.clone()).await?;
        tx.commit(ctx).await?;
        Ok(c)
    }

    /// AddItems appends facts to the blackboard of a change (artifacts, decisions): the nodes a change acts on are its
    /// change impacts (ProposeImpact, ADR 0024). Item ids are assigned when empty.
    pub async fn add_items(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        items: Vec<ChangeItem>,
    ) -> Result<Vec<ChangeItem>> {
        self.authorize_items(ctx, id, &items).await?;
        let mut out = Vec::with_capacity(items.len());
        let mut tx = self.change_tx(ctx, id).await?;
        let mut c = tx.change(ctx, id).await?;
        if c.status == CHANGE_APPLIED
            || c.status == CHANGE_ABANDONED
            || c.status == CHANGE_COMMITTED
        {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {id} is {}", c.status),
            ));
        }
        let mut known: std::collections::HashSet<ItemID> =
            c.items.iter().map(|it| it.id.clone()).collect();
        let mut items = items;
        for it in &mut items {
            if it.r#type != TYPE_FLOW {
                it.flow = c.resolve_flow(&it.flow); // no flow: the active option (ADR 0032 §6)
            }
        }
        let batch_flow = items.first().map(|it| it.flow.clone()).unwrap_or_default();
        for it in &items {
            if it.r#type == TYPE_FLOW {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    "flow events are recorded by OpenFlow, AdoptFlow and DiscardFlow",
                ));
            }
            if it.r#type == TYPE_TRANSITION {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    "transitions are recorded by TransitionChange",
                ));
            }
            if it.r#type == TYPE_DECISION_POINT {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    "decision points are recorded by OpenDecision, RuleDecision, AnswerQuestion and RatifyDecision",
                ));
            }
            if it.r#type == TYPE_REVIEW
                && review_record_of(&it.data).status == REVIEW_STATUS_SUBMITTED
            {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    "a review is submitted with its verdicts, by ImpactNodeReviewBatch (ADR 0105)",
                ));
            }
            if it.flow != batch_flow {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    "the items of a batch belong to one flow",
                ));
            }
        }
        if !batch_flow.is_empty() && c.flow_status_of(&batch_flow) != FLOW_OPEN {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("flow {batch_flow} is not open"),
            ));
        }
        for it in &mut items {
            if it.id.is_empty() {
                it.id = ItemID::new(self.new_id());
            }
        }
        for mut it in items {
            if it.status.is_empty() {
                it.status = ITEM_PROPOSED;
            }
            it.created_at = self.now();
            if let Err(e) = self.check_item(&it) {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!("item {}: {e}", it.id),
                ));
            }
            if let Some(d) = &it.decision
                && !known.contains(&d.item)
            {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!("decision {} targets unknown item {}", it.id, d.item),
                ));
            }
            put_item(ctx, &mut tx, id, &it).await?;
            known.insert(it.id.clone());
            out.push(it);
        }
        if c.status == CHANGE_DRAFT {
            c.status = CHANGE_ACTIVE;
            tx.put_change(ctx, c).await?;
        }
        tx.commit(ctx).await?;
        Ok(out)
    }

    /// authorizeItems asks ItemAuthorizer about the items of kinds that ask a permission, and ItemPolicy about every
    /// item, before any transaction.
    async fn authorize_items(&self, ctx: &Ctx, id: &ChangeID, items: &[ChangeItem]) -> Result<()> {
        if self.item_authorizer.is_none() && self.item_policy.is_none() {
            return Ok(());
        }
        let mut c: Option<Change> = None;
        for it in items {
            if let Some(p) = &self.item_policy {
                if c.is_none() {
                    c = Some(self.change(ctx, id).await?);
                }
                if let Err(e) = p.check(ctx, c.as_ref().expect("read above"), it).await {
                    return Err(Error::invalid(format!("{e}: invalid")));
                }
            }
            let Some(perm) = item_permission_of(&it.r#type) else {
                continue;
            };
            let Some(a) = &self.item_authorizer else {
                continue;
            };
            if c.is_none() {
                c = Some(self.change(ctx, id).await?);
            }
            a.authorize(ctx, c.as_ref().expect("read above"), it, &perm)
                .await?;
        }
        Ok(())
    }

    /// ChangeImpacts lists the versions a change starts from: the pre version of each of its change nodes.
    pub async fn change_impacts(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<NodeRef>> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        tx.commit(ctx).await?;
        Ok(c.nodes.into_iter().filter_map(|cn| cn.pre).collect())
    }

    /// openChangeHolders maps each node to the unapplied changes acting on it.
    pub(crate) async fn open_change_holders(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
    ) -> Result<BTreeMap<NodeID, Vec<ChangeID>>> {
        let ids = tx.open_change_ids(ctx).await?;
        let mut out: BTreeMap<NodeID, Vec<ChangeID>> = BTreeMap::new();
        for id in ids {
            let c = tx.change(ctx, &id).await?;
            for cn in &c.nodes {
                for r in [&cn.pre, &cn.post].into_iter().flatten() {
                    let list = out.entry(r.id.clone()).or_default();
                    if !list.contains(&id) {
                        list.push(id.clone());
                    }
                }
            }
        }
        Ok(out)
    }

    /// NodeChanges lists the changes acting on a node, oldest first.
    pub async fn node_changes(&self, ctx: &Ctx, node: &NodeID) -> Result<Vec<Change>> {
        let mut tx = self.begin(ctx).await?;
        let stored = tx.node_change_impacts(ctx, node).await?;
        let open = self.open_change_holders(ctx, &mut tx).await?;
        let mut out: Vec<Change> = Vec::new();
        for id in stored
            .iter()
            .chain(open.get(node).map(Vec::as_slice).unwrap_or_default())
        {
            if out.iter().any(|c| &c.id == id) {
                continue;
            }
            let mut c = tx.change(ctx, id).await?;
            c.items = Vec::new();
            c.nodes = Vec::new();
            out.push(c);
        }
        tx.commit(ctx).await?;
        out.sort_by_key(|c| c.created_at);
        Ok(out)
    }
}

/// view hydrates a node version with its neighbourhood.
pub(crate) async fn view(ctx: &Ctx, tx: &mut dyn Tx, r: &NodeRef) -> Result<NodeView> {
    let n = tx.node(ctx, r).await?;
    let latest = match tx
        .node(
            ctx,
            &NodeRef {
                id: r.id.clone(),
                version: 0,
            },
        )
        .await
    {
        Ok(l) => l,
        Err(e) if e.is_not_found() => n.clone(), // a node the change created, not on main yet
        Err(e) => return Err(e),
    };
    let out = tx.out_links(ctx, &n.r#ref()).await?;
    let r#in = tx.in_links(ctx, &n.r#ref()).await?;
    Ok(NodeView {
        node: n,
        latest: latest.version,
        out,
        r#in,
        ..Default::default()
    })
}

/// baselineGraphTx reads the nodes and links of a baseline.
pub(crate) async fn baseline_graph_tx(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    id: &BaselineID,
) -> Result<(Vec<Node>, Vec<Link>)> {
    let b = tx.baseline(ctx, id).await?;
    let nodes = tx.nodes_in(ctx, id, "").await?;
    let links = links_within(ctx, tx, &b, &nodes).await?;
    Ok((nodes, links))
}

/// linksWithin returns the outgoing links of the nodes whose target is in b.
pub(crate) async fn links_within(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    b: &Baseline,
    nodes: &[Node],
) -> Result<Vec<Link>> {
    let mut links = Vec::new();
    for n in nodes {
        for l in tx.out_links(ctx, &n.r#ref()).await? {
            if b.contains(&l.to) {
                links.push(l);
            }
        }
    }
    Ok(links)
}

/// validStatusMove is the change status machine: draft → active → committed → applied (committing and integrating
/// are done by CommitChange, IntegrateChange and Apply) or abandoned; applied and abandoned are final.
pub(crate) fn valid_status_move(from: &ChangeStatus, to: &ChangeStatus) -> bool {
    if *from == CHANGE_DRAFT {
        return *to == CHANGE_ACTIVE || *to == CHANGE_ABANDONED;
    }
    if *from == CHANGE_ACTIVE {
        return *to == CHANGE_DRAFT || *to == CHANGE_ABANDONED;
    }
    if *from == CHANGE_COMMITTED {
        return *to == CHANGE_ABANDONED;
    }
    false
}

/// The facet values of a blackboard, by name (the built-in ones and the providers'): what `Graph::facets` gives.
pub type Facets = std::collections::HashMap<String, FacetValue>;
