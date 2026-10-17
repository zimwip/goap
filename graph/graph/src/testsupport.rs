//! Shared test helpers (Go: `repos_test.go`, `baselinedelta_test.go`, `structure_test.go`, the fixtures of the
//! tests): the repositories every test runs against ([`for_each_repo`]: memory, SQLite in a temporary file, and
//! PostgreSQL in a fresh schema when GOAP_TEST_PG_DSN is set), the checks run after each test
//! ([`check_impact_logs`], [`check_landings`], [`check_states`], [`check_verdict_causes`]) and the fixtures that
//! arrange a world with the primitives of the storage ([`seed_node`], [`pin_baseline`], [`land`]). The later slices
//! of the port reuse them (and replace [`land`] by the real commit once it exists).

use std::collections::{BTreeMap, HashMap};
use std::path::PathBuf;
use std::sync::Arc;

use chrono::{DurationRound, TimeDelta};
use goap_algo::Bound;
use goap_ctx::Ctx;
use goap_domain::{AttributeCheck, Lifecycle, RequiredLink, SearchProperty, Structure, Structures};
use goap_domain::{
    BASELINE_COMMIT, BASELINE_FAST_FORWARD, Baseline, BaselineID, CHANGE_APPLIED, Change, ChangeID,
    ChangeImpact, ChangeImpactID, ChangeItem, IMPACT_LANDED, IMPACT_PROPOSED, IMPACT_REVIEWED,
    INTENT_CREATED, INTENT_MODIFIED, ImpactEvent, LOG_FACT, LOG_IMPACT, Link, LinkID, LogFilter,
    MAIN_BRANCH, Node, NodeID, NodeRef, REASON_CREATE, REASON_REVISE, REVIEW_ACCEPTED,
    REVIEW_REJECTED, REVIEW_STATUS_SUBMITTED, STRUCTURE_ORGANISATION, STRUCTURE_PROJECT,
    TYPE_REVIEW, Version, derived_kind, fold_impacts, namespace_of, review_record_of,
};
use serde_json::{Map, Value};
use sqlx::{PgPool, SqlitePool};

use crate::changelog::impact_events;
use crate::merge::branch_head;
use crate::repo::{Repo, Tx};
use crate::{
    Error, ErrorKind, Graph, MIGRATIONS, NewChange, Postgres, Result, SQLITE_MIGRATIONS, Sqlite,
};

// ---- the names of the organisation domain the tests use (structure_test.go) ------------------------------------

pub(crate) const NAMESPACE_ORGANISATION: &str = "organisation";
pub(crate) const NODE_TYPE_ORG_UNIT: &str = "organisation@OrgUnit";
pub(crate) const NODE_TYPE_PROJECT_UNIT: &str = "organisation@ProjectUnit";
pub(crate) const NODE_TYPE_USER: &str = "organisation@User";
pub(crate) const LINK_MEMBER_OF: &str = "organisation@member_of";
pub(crate) const LINK_PART_OF: &str = "organisation@part_of";
pub(crate) const LINK_PROJECT_PART_OF: &str = "organisation@project_part_of";

/// rootOrg and rootProject are the keys of the roots the bootstrap creates, as the structures in force name them.
pub(crate) fn root_org(g: &Graph) -> String {
    g.structure(STRUCTURE_ORGANISATION).root
}

pub(crate) fn root_project(g: &Graph) -> String {
    g.structure(STRUCTURE_PROJECT).root
}

pub(crate) fn ctx() -> Ctx {
    Ctx::background()
}

/// must unwraps a result, failing the test with the error.
#[track_caller]
pub(crate) fn must<T>(r: Result<T>) -> T {
    match r {
        Ok(v) => v,
        Err(e) => panic!("{e}"),
    }
}

/// Asserts that a result is an error of a kind (Go's `errors.Is`: nothing to commit is invalid too).
#[track_caller]
pub(crate) fn assert_kind<T: std::fmt::Debug>(r: Result<T>, kind: ErrorKind, what: &str) {
    match r {
        Err(e) if e.kind() == kind || (kind == ErrorKind::Invalid && e.is_invalid()) => {}
        other => panic!("{what}: {other:?}, want {kind:?}"),
    }
}

// ---- the repositories ---------------------------------------------------------------------------------------------

/// The store under a test repository, for a test that reaches its database.
pub(crate) enum Store {
    Memory,
    Sqlite(SqlitePool),
    Postgres(PgPool),
}

/// TestRepo is a repository a test runs against, and what keeps it alive (the temporary file, the schema).
pub(crate) struct TestRepo {
    pub(crate) name: &'static str,
    pub(crate) repo: Arc<dyn Repo>,
    pub(crate) store: Store,
    _dir: Option<TempDir>,
    _pg: Option<goap_pgtest::PgTest>,
}

impl TestRepo {
    /// A graph on the repository.
    pub(crate) fn graph(&self) -> Graph {
        Graph::new(self.repo.clone())
    }
}

/// A temporary directory removed when it goes (no tempfile crate in the workspace).
struct TempDir(PathBuf);

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

/// The in-memory repository.
pub(crate) fn memory_repo() -> TestRepo {
    TestRepo {
        name: "memory",
        repo: Arc::new(crate::Memory::new()),
        store: Store::Memory,
        _dir: None,
        _pg: None,
    }
}

/// A SQLite repository in a temporary file, migrated.
pub(crate) async fn sqlite_repo() -> TestRepo {
    let dir = std::env::temp_dir().join(format!("goap-graph-test-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&dir).expect("temporary directory");
    let path = dir.join("goap.db");
    let pool = goap_platform::open_sqlite(&ctx(), path.to_str().expect("utf-8 path"))
        .await
        .expect("open sqlite");
    goap_platform::migrate_sqlite(&ctx(), &pool, "graph", &SQLITE_MIGRATIONS, "")
        .await
        .expect("migrate sqlite");
    TestRepo {
        name: "sqlite",
        repo: Arc::new(Sqlite::new(pool.clone())),
        store: Store::Sqlite(pool),
        _dir: Some(TempDir(dir)),
        _pg: None,
    }
}

/// A PostgreSQL repository in a fresh schema, migrated, when GOAP_TEST_PG_DSN is set.
pub(crate) async fn postgres_repo() -> Option<TestRepo> {
    let dsn = std::env::var(goap_pgtest::DSN_VAR)
        .ok()
        .filter(|d| !d.is_empty())?;
    let pg = goap_pgtest::pool_with(&dsn, None).await;
    goap_platform::migrate(&ctx(), &pg, &MIGRATIONS, "")
        .await
        .expect("migrate postgres");
    let pool: PgPool = (*pg).clone();
    Some(TestRepo {
        name: "postgres",
        repo: Arc::new(Postgres::new(pool.clone())),
        store: Store::Postgres(pool),
        _dir: None,
        _pg: Some(pg),
    })
}

/// forEachRepo runs f against the in-memory repository, SQLite and, when GOAP_TEST_PG_DSN is set, PostgreSQL in a
/// fresh schema; after each it checks the log of every change against its projection, the landings and the states
/// of the baselines.
pub(crate) async fn for_each_repo(f: impl AsyncFn(&TestRepo)) {
    let mut repos = vec![memory_repo(), sqlite_repo().await];
    if let Some(pg) = postgres_repo().await {
        repos.push(pg);
    }
    for r in &repos {
        eprintln!("--- {}", r.name);
        Box::pin(f(r)).await;
        check_impact_logs(r.repo.clone()).await;
        check_landings(r.repo.clone()).await;
        check_states(r.repo.as_ref()).await;
    }
}

// ---- the checks ---------------------------------------------------------------------------------------------------

/// The change impacts as compared: times to the millisecond (what the stores keep), empty lists as absent.
fn norm(list: &[ChangeImpact]) -> String {
    let ms = TimeDelta::milliseconds(1);
    let out: Vec<ChangeImpact> = list
        .iter()
        .map(|cn| {
            let mut cn = cn.clone();
            cn.created_at = cn.created_at.duration_trunc(ms).unwrap_or(cn.created_at);
            for r in &mut cn.reviews {
                r.at = r.at.duration_trunc(ms).unwrap_or(r.at);
            }
            cn
        })
        .collect();
    serde_json::to_string_pretty(&out).expect("json")
}

/// checkImpactLogs replays the impact log of every change and compares it with the change_impact projection (ADR
/// 0029): nothing may write a projection without an event. It also compares the drafts a graph with an empty draft
/// cache reads with a from-scratch fold of the events (ADR 0079).
pub(crate) async fn check_impact_logs(repo: Arc<dyn Repo>) {
    let ctx = ctx();
    let cold = Graph::new(repo.clone());
    let mut tx = repo.begin(&ctx).await.expect("begin");
    let cs = tx.changes(&ctx).await.expect("changes");
    for c in &cs {
        let stored = tx.change_impacts(&ctx, &c.id).await.expect("impacts");
        let events = impact_events(&ctx, tx.as_mut(), &c.id)
            .await
            .expect("events");
        if let Err(e) = check_verdict_causes(&ctx, tx.as_mut(), &c.id).await {
            panic!("change {}: {e}", c.id);
        }
        let (got, want) = (norm(&fold_impacts(&events)), norm(&stored));
        assert_eq!(
            got,
            want,
            "change {}: the replay of its {} events differs from the projection",
            c.id,
            events.len()
        );
        let stored_drafts = cold.drafts(&ctx, tx.as_mut(), &c.id).await.expect("drafts");
        let mut replayed = goap_domain::fold_drafts(&events);
        replayed.sort_by(|a, b| a.impact.cmp(&b.impact).then_with(|| a.flow.cmp(&b.flow)));
        let dj = |l: &[goap_domain::Draft]| {
            if l.is_empty() {
                "[]".to_string()
            } else {
                serde_json::to_string_pretty(l).expect("json")
            }
        };
        assert_eq!(
            dj(&replayed),
            dj(&stored_drafts),
            "change {}: the folded drafts of its {} events differ from the from-scratch fold",
            c.id,
            events.len()
        );
    }
}

/// checkVerdictCauses checks that every verdict of a change was made by a submitted review (ADR 0105): a decided
/// reviewed event names as its cause an earlier fact.review entry, the submitted record of the review whose key the
/// verdict carries, holding the impact with that outcome.
pub(crate) async fn check_verdict_causes(ctx: &Ctx, tx: &mut dyn Tx, id: &ChangeID) -> Result<()> {
    let entries = tx
        .log(
            ctx,
            &LogFilter {
                change: id.clone(),
                ..Default::default()
            },
        )
        .await?;
    let mut seen = HashMap::new();
    for e in &entries {
        seen.insert(e.id.clone(), e.clone());
        if e.r#type != format!("{LOG_IMPACT}.{IMPACT_REVIEWED}") {
            continue;
        }
        let ev: ImpactEvent = serde_json::from_value(e.payload.clone())?;
        let Some(r) = &ev.review else { continue };
        if r.status != REVIEW_ACCEPTED && r.status != REVIEW_REJECTED {
            continue;
        }
        let cause = seen
            .get(&e.causation)
            .filter(|c| c.r#type == format!("{LOG_FACT}.{TYPE_REVIEW}"));
        let Some(cause) = cause else {
            return Err(Error::msg(format!(
                "verdict {} on {} has no earlier review as its cause ({:?})",
                e.seq, ev.impact, e.causation
            )));
        };
        let it: ChangeItem = serde_json::from_value(cause.payload.clone())?;
        let rec = review_record_of(&it.data);
        let entry = rec.entries.iter().find(|x| x.impact == ev.impact.as_str());
        let ok = rec.status == REVIEW_STATUS_SUBMITTED
            && rec.key == r.review_id
            && entry.is_some_and(|x| x.outcome.as_str() == goap_domain::review_outcome(&r.status));
        if !ok {
            return Err(Error::msg(format!(
                "verdict {} on {} (review {}) is not recorded by its cause {rec:?}",
                e.seq, ev.impact, r.review_id
            )));
        }
    }
    Ok(())
}

/// checkLandings verifies that every landed event agrees with the baseline it names (ADR 0032): the version is in it
/// (a tombstone: the node is not), and the baseline changed it from its parent. It reads through the guard: a
/// baseline kept as a header only is computed.
pub(crate) async fn check_landings(repo: Arc<dyn Repo>) {
    let ctx = ctx();
    let g = Graph::new(repo);
    let mut tx = g.begin(&ctx).await.expect("begin");
    let cs = tx.changes(&ctx).await.expect("changes");
    for c in &cs {
        let events = impact_events(&ctx, &mut tx, &c.id).await.expect("events");
        for e in events {
            if e.op != IMPACT_LANDED || e.baseline.is_empty() {
                continue;
            }
            let b = tx.baseline(&ctx, &e.baseline).await.expect("baseline");
            let parent = if b.parent_id.is_empty() {
                BTreeMap::new()
            } else {
                tx.baseline(&ctx, &b.parent_id).await.expect("parent").nodes
            };
            let n = tx
                .node(&ctx, e.landed.as_ref().expect("landed"))
                .await
                .expect("node");
            let v = b.nodes.get(&n.id);
            if let (true, Some(v)) = (n.deleted, v) {
                panic!(
                    "change {}: {} landed as a tombstone in {}, which holds v{}",
                    c.id, n.key, b.id, v
                );
            }
            if !n.deleted && v != Some(&n.version) {
                panic!(
                    "change {}: {} landed as v{} in {}, which holds {v:?}",
                    c.id, n.key, n.version, b.id
                );
            }
            if !n.deleted && parent.get(&n.id) == Some(&n.version) {
                panic!(
                    "change {}: {} landed as v{} in {}, already in its parent",
                    c.id, n.key, n.version, b.id
                );
            }
        }
    }
    drop(tx);
}

/// checkStates verifies that what the changes did tells the state of every baseline (ADR 0056): replaying from the
/// state of its parent the `landed` events of a commit baseline, or the versions the change joined to the branch
/// for a fast-forward, gives exactly the entries stored for it, whatever the baselines in between were stored as
/// (an entry-less baseline is a header only). A snapshot is not derived and is skipped. It reads the store itself.
pub(crate) async fn check_states(repo: &dyn Repo) {
    let ctx = ctx();
    let mut tx = repo.begin(&ctx).await.expect("begin");
    let nss = tx.namespaces(&ctx).await.expect("namespaces");
    let mut by_id: HashMap<BaselineID, Baseline> = HashMap::new();
    let mut memo: HashMap<BaselineID, BTreeMap<NodeID, Version>> = HashMap::new();
    let mut all = Vec::new();
    for ns in &nss {
        let bs = tx.baselines(&ctx, ns).await.expect("baselines");
        for b in &bs {
            by_id.insert(b.id.clone(), b.clone());
        }
        all.extend(bs);
    }
    for b in &all {
        let got = state_of(&ctx, tx.as_mut(), b, &by_id, &mut memo)
            .await
            .unwrap_or_else(|e| panic!("{e}"));
        if b.gap == 0 && got != b.nodes {
            panic!(
                "baseline {} ({}, {}): replayed {:?}, stored {:?}",
                b.id, b.name, b.kind, got, b.nodes
            );
        }
    }
}

async fn state_of(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    b: &Baseline,
    by_id: &HashMap<BaselineID, Baseline>,
    memo: &mut HashMap<BaselineID, BTreeMap<NodeID, Version>>,
) -> Result<BTreeMap<NodeID, Version>> {
    // the chain up to a baseline whose state is known (stored whole, or memoised)
    let mut chain = vec![b.clone()];
    let mut from = loop {
        let cur = chain.last().expect("non-empty");
        if let Some(s) = memo.get(&cur.id) {
            let s = s.clone();
            chain.pop();
            break s;
        }
        if !derived_kind(&cur.kind) {
            let s = cur.nodes.clone();
            chain.pop();
            break s;
        }
        if cur.parent_id.is_empty() {
            break BTreeMap::new();
        }
        let Some(p) = by_id.get(&cur.parent_id) else {
            return Err(Error::msg(format!(
                "baseline {}: parent {} is unknown",
                cur.id, cur.parent_id
            )));
        };
        chain.push(p.clone());
    };
    for b in chain.iter().rev() {
        let mut out = from.clone();
        if b.kind != BASELINE_FAST_FORWARD {
            let events = impact_events(ctx, tx, &b.change_id).await?;
            for e in events {
                if e.op == IMPACT_LANDED
                    && e.baseline == b.id
                    && let Some(l) = &e.landed
                {
                    put(ctx, tx, &mut out, l).await?;
                }
            }
        } else {
            let refs = tx
                .branch_joins(ctx, &b.namespace, &b.branch, &b.change_id)
                .await?;
            let mut latest: BTreeMap<NodeID, NodeRef> = BTreeMap::new();
            for r in refs {
                if r.version > latest.get(&r.id).map(|x| x.version).unwrap_or(0) {
                    latest.insert(r.id.clone(), r);
                }
            }
            for r in latest.values() {
                put(ctx, tx, &mut out, r).await?;
            }
        }
        memo.insert(b.id.clone(), out.clone());
        from = out;
    }
    Ok(from)
}

async fn put(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    out: &mut BTreeMap<NodeID, Version>,
    r: &NodeRef,
) -> Result<()> {
    let n = tx.node(ctx, r).await?;
    if n.deleted {
        out.remove(&n.id);
    } else {
        out.insert(n.id, n.version);
    }
    Ok(())
}

// ---- fixtures -------------------------------------------------------------------------------------------------

/// A node a fixture writes, with its outgoing links.
#[derive(Default, Clone)]
pub(crate) struct NewNode {
    pub(crate) namespace: String,
    pub(crate) key: String,
    pub(crate) r#type: String,
    pub(crate) properties: Map<String, Value>,
    pub(crate) state: String,
    pub(crate) links: Vec<(String, NodeRef)>,
}

/// A change opened on the current head of a namespace's main branch (the empty state if none), in the root project.
pub(crate) async fn test_change(g: &Graph, namespace: &str) -> Change {
    let head = must(g.branch_head(&ctx(), namespace, MAIN_BRANCH).await);
    must(
        g.create_change(
            &ctx(),
            NewChange {
                project_id: "PROJ-ROOT".into(),
                namespace: namespace.into(),
                baseline_id: head.id,
                title: "test".into(),
                ..Default::default()
            },
        )
        .await,
    )
}

/// seedNode writes a node version and its outgoing links directly, inside a change of its own left open (ADR 0054):
/// fixtures use it to arrange a world already sitting in a given state. The version is on main, not part of any
/// baseline ([`pin_baseline`] makes it part of the head of main).
pub(crate) async fn seed_node(g: &Graph, input: NewNode) -> Result<Node> {
    let ctx = ctx();
    let mut n = Node {
        id: NodeID::new(g.new_id()),
        version: 1,
        branch: MAIN_BRANCH.into(),
        reason: REASON_CREATE.into(),
        namespace: namespace_of(&input.namespace).to_string(),
        key: input.key.clone(),
        r#type: input.r#type.clone(),
        properties: input.properties.clone(),
        created_at: g.now(),
        state: input.state.clone(),
        ..Default::default()
    };
    if n.key.is_empty() {
        n.key = n.id.to_string();
    }
    let head = g.branch_head(&ctx, &n.namespace, MAIN_BRANCH).await?;
    let c = g
        .create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                namespace: n.namespace.clone(),
                title: format!("seed {}", n.key),
                baseline_id: head.id,
                ..Default::default()
            },
        )
        .await?;
    n.change_id = c.id.clone();
    let r = n.r#ref();
    let mut tx = g.begin(&ctx).await?;
    tx.put_node(&ctx, n).await?;
    for (typ, to) in input.links {
        tx.put_link(
            &ctx,
            Link {
                id: LinkID::new(g.new_id()),
                r#type: typ,
                from: r.clone(),
                to,
                change_id: c.id.clone(),
                ..Default::default()
            },
        )
        .await?;
    }
    tx.commit(&ctx).await?;
    g.node(&ctx, &r).await
}

/// pinBaseline makes the head of main hold exactly the given versions on top of what it holds, through an applied
/// change (a snapshot): the fixtures that arrange a world with seed_node need them part of the state a change starts
/// from.
pub(crate) async fn pin_baseline(g: &Graph, namespace: &str, refs: &[NodeRef]) -> Result<Baseline> {
    let ctx = ctx();
    g.bootstrap(&ctx).await?;
    let namespace = namespace_of(namespace).to_string();
    let mut tx = g.begin(&ctx).await?;
    let head = branch_head(&ctx, &mut tx, &namespace, MAIN_BRANCH).await?;
    let mut nodes = head.nodes.clone();
    for r in refs {
        let n = tx.node(&ctx, r).await?;
        if n.namespace != namespace {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "node {} is of namespace {}, not {namespace}",
                    n.key, n.namespace
                ),
            ));
        }
        if !n.deleted {
            nodes.insert(n.id, n.version);
        }
    }
    let mut c = Change {
        id: ChangeID::new(g.new_id()),
        title: "Pin".into(),
        namespace: namespace.clone(),
        status: CHANGE_APPLIED,
        intent: "Pin node versions for a fixture".into(),
        baseline_id: head.id.clone(),
        branch: MAIN_BRANCH.into(),
        owner_org: root_org(g),
        project_id: root_project(g),
        created_at: g.now(),
        ..Default::default()
    };
    tx.put_change(&ctx, c.clone()).await?;
    let out = Baseline {
        id: BaselineID::new(g.new_id()),
        name: "Pin".into(),
        namespace: namespace.clone(),
        branch: MAIN_BRANCH.into(),
        parent_id: head.id.clone(),
        change_id: c.id.clone(),
        nodes,
        created_at: g.now(),
        ..Default::default()
    };
    tx.put_baseline(&ctx, out.clone()).await?;
    g.advance_branch(&ctx, &mut tx, &namespace, MAIN_BRANCH, &out)
        .await?;
    c.result_baseline_id = out.id.clone();
    tx.put_change(&ctx, c).await?;
    tx.commit(&ctx).await?;
    Ok(out)
}

/// A node a [`land`] writes: by key, created or a new version of the node of that key; deleted lands a tombstone.
#[derive(Default, Clone)]
pub(crate) struct Landing {
    pub(crate) key: String,
    pub(crate) r#type: String,
    pub(crate) properties: Map<String, Value>,
    pub(crate) deleted: bool,
    /// Outgoing links of the new version: (link type, key of the target), the target the version this landing
    /// writes for it, else its latest on main.
    pub(crate) links: Vec<(String, String)>,
}

pub(crate) fn landing(key: &str, typ: &str) -> Landing {
    Landing {
        key: key.into(),
        r#type: typ.into(),
        ..Default::default()
    }
}

/// land lands nodes on main as a commit would, without the checks of Commit / Apply (a fixture of the Rust tests): one
/// applied change from the head of main, the versions written, a change impact per node (a `proposed` event) and
/// its `landed` event naming the baseline, a commit baseline, the head moved. The state of the baseline is derived
/// from the landed events (ADR 0056), so it is stored as a header only between two materialised ones.
pub(crate) async fn land(g: &Graph, namespace: &str, nodes: &[Landing]) -> Result<Baseline> {
    let ctx = ctx();
    let namespace = namespace_of(namespace).to_string();
    let head = g.branch_head(&ctx, &namespace, MAIN_BRANCH).await?;
    let c = g
        .create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                namespace: namespace.clone(),
                title: "land".into(),
                baseline_id: head.id.clone(),
                ..Default::default()
            },
        )
        .await?;
    let mut tx = g.change_tx(&ctx, &c.id).await?;
    let b_id = BaselineID::new(g.new_id());
    let mut state = head.nodes.clone();
    let mut events = Vec::new();
    let mut written: HashMap<String, NodeRef> = HashMap::new();
    for l in nodes {
        let (id, pre) = match tx.node_id_by_key(&ctx, &namespace, &l.key).await {
            Ok(id) => {
                let cur = tx.latest_on(&ctx, &id, MAIN_BRANCH).await?;
                (id, Some(cur.r#ref()))
            }
            Err(e) if e.is_not_found() => (NodeID::new(g.new_id()), None),
            Err(e) => return Err(e),
        };
        let version = if pre.is_some() {
            crate::apply::next_version(&ctx, &mut tx, &id).await?
        } else {
            1
        };
        let n = Node {
            id: id.clone(),
            version,
            branch: MAIN_BRANCH.into(),
            parents: pre.iter().map(|p| p.version).collect(),
            reason: if pre.is_some() {
                REASON_REVISE
            } else {
                REASON_CREATE
            }
            .into(),
            namespace: namespace.clone(),
            key: l.key.clone(),
            r#type: l.r#type.clone(),
            properties: l.properties.clone(),
            deleted: l.deleted,
            change_id: c.id.clone(),
            created_at: g.now(),
            ..Default::default()
        };
        let r = n.r#ref();
        tx.put_node(&ctx, n).await?;
        written.insert(l.key.clone(), r.clone());
        if l.deleted {
            state.remove(&id);
        } else {
            state.insert(id.clone(), version);
        }
        let impact = ChangeImpactID::new(g.new_id());
        events.push(ImpactEvent {
            change: c.id.clone(),
            impact: impact.clone(),
            op: IMPACT_PROPOSED,
            state: Some(ChangeImpact {
                id: impact.clone(),
                key: l.key.clone(),
                r#type: l.r#type.clone(),
                intent: if pre.is_some() {
                    INTENT_MODIFIED
                } else {
                    INTENT_CREATED
                },
                rationale: "land".into(),
                pre: pre.clone(),
                created_at: g.now(),
                ..Default::default()
            }),
            ..Default::default()
        });
        events.push(ImpactEvent {
            change: c.id.clone(),
            impact,
            op: IMPACT_LANDED,
            landed: Some(r),
            baseline: b_id.clone(),
            branch: MAIN_BRANCH.into(),
            ..Default::default()
        });
    }
    for l in nodes {
        for (typ, to) in &l.links {
            let to = match written.get(to) {
                Some(r) => r.clone(),
                None => {
                    let id = tx.node_id_by_key(&ctx, &namespace, to).await?;
                    tx.latest_on(&ctx, &id, MAIN_BRANCH).await?.r#ref()
                }
            };
            tx.put_link(
                &ctx,
                Link {
                    id: LinkID::new(g.new_id()),
                    r#type: typ.clone(),
                    from: written[&l.key].clone(),
                    to,
                    change_id: c.id.clone(),
                    ..Default::default()
                },
            )
            .await?;
        }
    }
    g.emit(&ctx, &mut tx, events).await?;
    let b = Baseline {
        id: b_id,
        name: "land".into(),
        namespace: namespace.clone(),
        branch: MAIN_BRANCH.into(),
        parent_id: head.id.clone(),
        change_id: c.id.clone(),
        kind: BASELINE_COMMIT.into(),
        nodes: state,
        created_at: g.now(),
        ..Default::default()
    };
    tx.put_baseline(&ctx, b.clone()).await?;
    g.advance_branch(&ctx, &mut tx, &namespace, MAIN_BRANCH, &b)
        .await?;
    let mut c = tx.change(&ctx, &c.id).await?;
    c.status = CHANGE_APPLIED;
    c.result_baseline_id = b.id.clone();
    tx.put_change(&ctx, c).await?;
    tx.commit(&ctx).await?;
    Ok(b)
}

/// A map of properties from pairs.
pub(crate) fn props(pairs: &[(&str, Value)]) -> Map<String, Value> {
    pairs
        .iter()
        .map(|(k, v)| (k.to_string(), v.clone()))
        .collect()
}

/// The world most tests start from (Go's `newFixture`): NEED-1, REQ-1 satisfying it and TST-1 verifying REQ-1, landed
/// on main of the default namespace; base is the head of main after them.
pub(crate) struct Fixture {
    pub(crate) g: Graph,
    pub(crate) need: Node,
    pub(crate) req: Node,
    pub(crate) test: Node,
    pub(crate) base: Baseline,
}

pub(crate) async fn new_fixture(r: &TestRepo) -> Fixture {
    use crate::tests::helpers::{ImportNode, import_node};
    use goap_graphapi::LinkWrite;
    let g = r.graph();
    must(g.bootstrap(&ctx()).await);
    let need = must(
        import_node(
            &g,
            ImportNode {
                key: "NEED-1".into(),
                r#type: "Need".into(),
                properties: props(&[("title", "Pay online".into())]),
                ..Default::default()
            },
        )
        .await,
    );
    let req = must(
        import_node(
            &g,
            ImportNode {
                key: "REQ-1".into(),
                r#type: "Requirement".into(),
                properties: props(&[("title", "Use PSP v1".into())]),
                links: vec![LinkWrite {
                    r#type: "satisfies".into(),
                    to: need.r#ref(),
                    ..Default::default()
                }],
                ..Default::default()
            },
        )
        .await,
    );
    let test = must(
        import_node(
            &g,
            ImportNode {
                key: "TST-1".into(),
                r#type: "TestCase".into(),
                links: vec![LinkWrite {
                    r#type: "verifies".into(),
                    to: req.r#ref(),
                    ..Default::default()
                }],
                ..Default::default()
            },
        )
        .await,
    );
    let base = must(g.branch_head(&ctx(), "", MAIN_BRANCH).await);
    Fixture {
        g,
        need,
        req,
        test,
        base,
    }
}

// ---- a type catalogue without a domain (Go's testTypes, typecat_test.go) ---------------------------------------

/// A node type of [`TestTypes`].
#[derive(Default, Clone)]
pub(crate) struct TestType {
    pub(crate) extends: String,
    pub(crate) lifecycle: Option<Lifecycle>,
    pub(crate) validators: Vec<Bound>,
    pub(crate) search: Vec<SearchProperty>,
}

/// TestTypes is a TypeCatalog for the tests that need types without a domain: names are taken as they are, a known
/// type passes the existence rule, links are not checked; structures, required links, admin-only types and item
/// types are the built-in ones. What a type inherits is resolved when it is built.
pub(crate) struct TestTypes {
    types: HashMap<String, TestType>,
    lifecycles: HashMap<String, Lifecycle>,
    validators: HashMap<String, Vec<Bound>>,
    search: HashMap<String, Vec<SearchProperty>>,
    builtin: Arc<goap_typecat::Catalog>,
}

impl TestTypes {
    pub(crate) fn new(types: &[(&str, TestType)]) -> TestTypes {
        let types: HashMap<String, TestType> = types
            .iter()
            .map(|(k, t)| (k.to_string(), t.clone()))
            .collect();
        let chain = |typ: &str| {
            let mut out: Vec<&TestType> = Vec::new();
            let mut seen = std::collections::HashSet::new();
            let mut cur = typ.to_string();
            while !cur.is_empty() && seen.insert(cur.clone()) {
                let Some(t) = types.get(&cur) else { break };
                out.push(t);
                cur = t.extends.clone();
            }
            out
        };
        let (mut lifecycles, mut validators, mut search) =
            (HashMap::new(), HashMap::new(), HashMap::new());
        for name in types.keys() {
            let ch = chain(name);
            if let Some(lc) = ch.iter().find_map(|t| t.lifecycle.clone()) {
                lifecycles.insert(name.clone(), lc);
            }
            let mut vs = Vec::new();
            let mut ss: Vec<SearchProperty> = Vec::new();
            for t in ch.iter().rev() {
                vs.extend(t.validators.iter().cloned());
                for s in &t.search {
                    match ss.iter_mut().find(|x| x.property == s.property) {
                        Some(x) => *x = s.clone(),
                        None => ss.push(s.clone()),
                    }
                }
            }
            validators.insert(name.clone(), vs);
            search.insert(name.clone(), ss);
        }
        TestTypes {
            types,
            lifecycles,
            validators,
            search,
            builtin: goap_typecat::builtin(),
        }
    }
}

impl crate::TypeCatalog for TestTypes {
    fn lifecycle(&self, typ: &str) -> Option<&Lifecycle> {
        self.lifecycles.get(typ)
    }
    fn validators(&self, typ: &str) -> &[Bound] {
        self.validators
            .get(typ)
            .map(|v| v.as_slice())
            .unwrap_or(&[])
    }
    fn attribute_checks(&self, _: &str) -> Vec<AttributeCheck> {
        Vec::new()
    }
    fn link_attribute_checks(&self, _: &str) -> Vec<AttributeCheck> {
        Vec::new()
    }
    fn link_attribute_validators(&self, _: &str) -> &[Bound] {
        &[]
    }
    fn link_type_validators(&self, _: &str) -> &[Bound] {
        &[]
    }
    fn attribute_names(&self, _: &str) -> (Vec<String>, bool) {
        (Vec::new(), true)
    }
    fn search(&self, typ: &str) -> &[SearchProperty] {
        self.search.get(typ).map(|v| v.as_slice()).unwrap_or(&[])
    }
    fn check_node(&self, _: &str, typ: &str) -> std::result::Result<(), String> {
        if self.types.contains_key(typ) {
            Ok(())
        } else {
            Err(format!("unknown type {typ}"))
        }
    }
    fn check_link(&self, _: &str, _: &str, _: &str) -> std::result::Result<(), String> {
        Ok(())
    }
    // Structure resolves none: the graph falls back to the built-in structures.
    fn structure(&self, _: &str) -> Option<&Structure> {
        None
    }
    fn is_a(&self, typ: &str, base: &str) -> bool {
        let mut seen = std::collections::HashSet::new();
        let mut cur = typ.to_string();
        while !cur.is_empty() && seen.insert(cur.clone()) {
            if cur == base {
                return true;
            }
            cur = self
                .types
                .get(&cur)
                .map(|t| t.extends.clone())
                .unwrap_or_default();
        }
        false
    }
    fn structures(&self) -> Structures {
        self.builtin.structures()
    }
    fn requires(&self, _: &str) -> &[RequiredLink] {
        &[]
    }
    fn admin_only(&self, _: &str) -> bool {
        false
    }
    fn composes(&self, _: &str) -> bool {
        false
    }
    fn item_attribute_checks(&self, t: &str) -> Option<Vec<AttributeCheck>> {
        self.builtin.item_attribute_checks(t)
    }
    fn item_types(&self) -> Vec<&goap_typecat::ItemType> {
        self.builtin.item_types()
    }
}

/// Makes a graph judge by a catalogue.
pub(crate) fn use_types(g: &mut Graph, cat: Arc<dyn crate::TypeCatalog>) {
    g.set_types(move || Some(cat.clone()));
}

// ---- the commit and the merge of the tests --------------------------------------------------------------------

/// commitChange is Go's `CommitChange` as the tests call it.
pub(crate) async fn commit_change(g: &Graph, id: &ChangeID, name: &str) -> Result<Baseline> {
    g.commit_change(&ctx(), id, name).await
}

/// apply is Go's `Apply` as the tests call it.
pub(crate) async fn apply(g: &Graph, id: &ChangeID, name: &str) -> Result<Baseline> {
    g.apply(&ctx(), id, name).await
}

/// integrateChange is Go's `IntegrateChange` with no resolution, as the tests call it.
pub(crate) async fn integrate_change(g: &Graph, id: &ChangeID) -> Result<Change> {
    g.integrate_change(&ctx(), id, &std::collections::BTreeMap::new())
        .await
}
