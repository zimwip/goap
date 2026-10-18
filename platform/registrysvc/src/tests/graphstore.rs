//! graphstore_test.go, over the graph in memory of the tests and over the real graph (goap-graph).

use chrono::Utc;
use goap_domain::MAIN_BRANCH;
use goap_methodology::{Method, Methodology, Process, Role, Step};

use std::sync::Arc;

use super::realgraph::{Probe, real};
use super::{FakeGraph, at, bg, canon, example};
use crate::graphstore::{
    GraphStore, LINK_DEFINES, LINK_SUB_ACTIVITY, NAMESPACE_METHODOLOGY, TYPE_METHODOLOGY_VERSION,
    methodology_version_key,
};
use crate::store::{Record, STATUS_DRAFT, Store};

fn draft(
    m: &Methodology,
    created: Option<chrono::DateTime<Utc>>,
    updated: chrono::DateTime<Utc>,
) -> Record {
    Record {
        methodology: m.clone(),
        status: STATUS_DRAFT,
        created_at: created,
        updated_at: Some(updated),
        ..Record::default()
    }
}

#[tokio::test]
async fn graph_store_round_trips_every_methodology_of_the_repository() {
    graph_store_round_trips_every_methodology_of_the_repository_over(FakeGraph::new()).await;
    graph_store_round_trips_every_methodology_of_the_repository_over(real()).await;
}

async fn graph_store_round_trips_every_methodology_of_the_repository_over<G: Probe + 'static>(
    g: Arc<G>,
) {
    let ctx = bg();
    let s = GraphStore::new(g);
    let now = at(2026, 9, 26, 10, 0);
    let mut files: Vec<_> = std::fs::read_dir("../../methodologies")
        .expect("dir")
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| p.extension().is_some_and(|x| x == "yaml"))
        .collect();
    files.sort();
    assert!(!files.is_empty());
    for f in files {
        let m = goap_methodology::load_file(&f).unwrap_or_else(|e| panic!("{}: {e}", f.display()));
        s.save(&ctx, draft(&m, Some(now), now))
            .await
            .unwrap_or_else(|e| panic!("{}: {e}", f.display()));
        let got = s.get(&ctx, &m.name, &m.version).await.expect("get");
        assert_eq!(
            canon(&got.methodology),
            canon(&m),
            "methodology {} does not round-trip",
            m.name
        );
    }
}

#[tokio::test]
async fn graph_store_edits_elements_not_documents() {
    graph_store_edits_elements_not_documents_over(FakeGraph::new()).await;
    graph_store_edits_elements_not_documents_over(real()).await;
}

async fn graph_store_edits_elements_not_documents_over<G: Probe + 'static>(g: Arc<G>) {
    let ctx = bg();
    let s = GraphStore::new(g.clone());
    let now = Utc::now();
    let mut m = example();
    m.version = "9.0.0".into();
    s.save(&ctx, draft(&m, Some(now), now)).await.expect("save");
    let version = async |k: &str| {
        g.find(NAMESPACE_METHODOLOGY, k)
            .await
            .unwrap_or_else(|| panic!("{k}"))
            .version
    };
    let hk = methodology_version_key(&m.name, &m.version);
    let ak = format!("{hk}/action/{}", m.actions[0].name);
    let other = format!("{hk}/action/{}", m.actions[1].name);
    let (a0, o0) = (version(&ak).await, version(&other).await);

    // editing one action gives a new version of that node only
    m.actions[0].description = "edited".into();
    s.save(&ctx, draft(&m, None, now)).await.expect("save");
    assert!(
        version(&ak).await == a0 + 1 && version(&other).await == o0,
        "only the edited action must move"
    );
    // unchanged content changes nothing
    let head = g
        .branch_head(&ctx, NAMESPACE_METHODOLOGY, MAIN_BRANCH)
        .await
        .expect("head");
    s.save(&ctx, draft(&m, None, now)).await.expect("save");
    let h2 = g
        .branch_head(&ctx, NAMESPACE_METHODOLOGY, MAIN_BRANCH)
        .await
        .expect("head");
    assert_eq!(
        h2.id, head.id,
        "saving the same definition must not change the graph"
    );
    // an element removed and added again is revived, not created twice
    let mut m2 = m.clone();
    m2.actions.pop();
    s.save(&ctx, draft(&m2, None, now)).await.expect("save");
    let got = s.get(&ctx, &m.name, &m.version).await.expect("get");
    assert_eq!(
        got.methodology.actions.len(),
        m2.actions.len(),
        "removed action still there"
    );
    s.save(&ctx, draft(&m, None, now))
        .await
        .expect("adding it back");
    let got = s.get(&ctx, &m.name, &m.version).await.expect("get");
    assert_eq!(
        canon(&got.methodology),
        canon(&m),
        "the definition must be back as it was"
    );
    // the elements are tied to their version
    let (nodes, links) = g.baseline_graph(&ctx, &head.id).await.expect("graph");
    let defines = links.iter().filter(|l| l.r#type == LINK_DEFINES).count();
    assert!(defines > 0 && !nodes.is_empty(), "expected defines links");
    // deleting a draft then saving it again works
    s.delete(&ctx, &m.name, &m.version).await.expect("delete");
    assert!(
        s.get(&ctx, &m.name, &m.version).await.is_err(),
        "deleted draft must be gone"
    );
    s.save(&ctx, draft(&m2, None, now)).await.expect("revive");
    let got = s.get(&ctx, &m.name, &m.version).await.expect("revived");
    assert_eq!(got.methodology.actions.len(), m2.actions.len());
}

#[tokio::test]
async fn graph_store_keeps_methodologies_in_their_namespace() {
    graph_store_keeps_methodologies_in_their_namespace_over(FakeGraph::new()).await;
    graph_store_keeps_methodologies_in_their_namespace_over(real()).await;
}

async fn graph_store_keeps_methodologies_in_their_namespace_over<G: Probe + 'static>(g: Arc<G>) {
    let ctx = bg();
    let s = GraphStore::new(g.clone());
    let now = Utc::now();
    let m = example();
    s.save(&ctx, draft(&m, Some(now), now)).await.expect("save");
    let k = methodology_version_key(&m.name, &m.version);
    let mh = g.find(NAMESPACE_METHODOLOGY, &k).await.expect("header");
    assert_eq!(mh.r#type, TYPE_METHODOLOGY_VERSION);
    assert!(
        g.find("platform", &k).await.is_none(),
        "nothing of the registry's storage belongs to the platform namespace"
    );
}

// A methodology's declared roles materialize as their own methodology@Role nodes (ADR 0035 §2 / ADR 0039).
#[tokio::test]
async fn graph_store_materializes_roles() {
    graph_store_materializes_roles_over(FakeGraph::new()).await;
    graph_store_materializes_roles_over(real()).await;
}

async fn graph_store_materializes_roles_over<G: Probe + 'static>(g: Arc<G>) {
    let ctx = bg();
    let s = GraphStore::new(g.clone());
    let now = Utc::now();
    let mut m = example();
    m.version = "9.1.0".into();
    m.roles = vec![Role {
        name: "developer".into(),
        description: "Implements the change".into(),
        ..Role::default()
    }];
    s.save(&ctx, draft(&m, Some(now), now)).await.expect("save");
    let key = format!(
        "{}/role/developer",
        methodology_version_key(&m.name, &m.version)
    );
    let n = g
        .find(NAMESPACE_METHODOLOGY, &key)
        .await
        .expect("role node");
    assert_eq!(n.r#type, "methodology@Role");
    assert_eq!(
        n.properties.get("description").and_then(|v| v.as_str()),
        Some("Implements the change")
    );
    let got = s.get(&ctx, &m.name, &m.version).await.expect("get");
    assert!(got.methodology.roles.len() == 1 && got.methodology.roles[0].name == "developer");
}

async fn has_link<G: Probe>(
    g: &G,
    links: &[goap_domain::Link],
    typ: &str,
    from_key: &str,
    to_key: &str,
) -> bool {
    let (Some(from), Some(to)) = (
        g.find(NAMESPACE_METHODOLOGY, from_key).await,
        g.find(NAMESPACE_METHODOLOGY, to_key).await,
    ) else {
        return false;
    };
    links
        .iter()
        .any(|l| l.r#type == typ && l.from.id == from.id && l.to.id == to.id)
}

// A process's steps and sub-steps materialize as their own methodology@Step nodes, and only there (ADR 0120).
#[tokio::test]
async fn graph_store_materializes_steps() {
    graph_store_materializes_steps_over(FakeGraph::new()).await;
    graph_store_materializes_steps_over(real()).await;
}

async fn graph_store_materializes_steps_over<G: Probe + 'static>(g: Arc<G>) {
    let ctx = bg();
    let s = GraphStore::new(g.clone());
    let now = Utc::now();
    let mut m = example();
    m.version = "9.2.0".into();
    let mut scope = Step::default();
    scope.name = "scope".into();
    scope.action = "identify_scope".into();
    let mut analysis = Step::default();
    analysis.name = "analysis".into();
    analysis.pre.insert("framed".into(), true);
    analysis.steps = vec![scope];
    let mut p = Process::default();
    p.name = "deliver".into();
    p.steps = vec![analysis];
    m.processes = vec![p];
    s.save(&ctx, draft(&m, Some(now), now)).await.expect("save");
    let hk = methodology_version_key(&m.name, &m.version);
    let n = g
        .find(
            NAMESPACE_METHODOLOGY,
            &format!("{hk}/step/deliver/analysis"),
        )
        .await
        .expect("step node");
    assert_eq!(n.r#type, "methodology@Step");
    assert_eq!(
        canon(&n.properties.get("input")),
        r#"{"framed":true}"#,
        "pre renamed to input"
    );
    assert!(
        !n.properties.contains_key("steps"),
        "nested steps are not duplicated onto the step node"
    );
    let sub = g
        .find(
            NAMESPACE_METHODOLOGY,
            &format!("{hk}/step/deliver/analysis/scope"),
        )
        .await
        .expect("sub-step node");
    assert_eq!(
        sub.properties.get("action").and_then(|v| v.as_str()),
        Some("identify_scope")
    );
    let head = g
        .branch_head(&ctx, NAMESPACE_METHODOLOGY, MAIN_BRANCH)
        .await
        .expect("head");
    let (_, links) = g.baseline_graph(&ctx, &head.id).await.expect("graph");
    assert!(
        has_link(
            &*g,
            &links,
            LINK_SUB_ACTIVITY,
            &format!("{hk}/process/deliver"),
            &format!("{hk}/step/deliver/analysis")
        )
        .await,
        "process -> step sub_activity link missing"
    );
    assert!(
        has_link(
            &*g,
            &links,
            LINK_SUB_ACTIVITY,
            &format!("{hk}/step/deliver/analysis"),
            &format!("{hk}/step/deliver/analysis/scope")
        )
        .await,
        "step -> sub-step sub_activity link missing"
    );
    // the steps read back from their nodes: the methodology round-trips to exactly what was saved
    let got = s.get(&ctx, &m.name, &m.version).await.expect("get");
    assert_eq!(canon(&got.methodology.processes), canon(&m.processes));
}

// A method composing its own steps materializes methodology@MethodStep nodes.
#[tokio::test]
async fn graph_store_materializes_method_steps() {
    graph_store_materializes_method_steps_over(FakeGraph::new()).await;
    graph_store_materializes_method_steps_over(real()).await;
}

async fn graph_store_materializes_method_steps_over<G: Probe + 'static>(g: Arc<G>) {
    let ctx = bg();
    let s = GraphStore::new(g.clone());
    let now = Utc::now();
    let mut m = example();
    m.version = "9.3.0".into();
    let step = |name: &str, action: &str| {
        let mut st = Step::default();
        st.name = name.into();
        st.action = action.into();
        st
    };
    let mut me = Method::default();
    me.name = "inspect".into();
    me.r#for = "verification".into();
    me.steps = vec![step("note", "write_note"), step("verify", "check")];
    m.methods = vec![me];
    s.save(&ctx, draft(&m, Some(now), now)).await.expect("save");
    let hk = methodology_version_key(&m.name, &m.version);
    let n = g
        .find(
            NAMESPACE_METHODOLOGY,
            &format!("{hk}/methodStep/inspect/note"),
        )
        .await
        .expect("method step node");
    assert_eq!(n.r#type, "methodology@MethodStep");
    assert_eq!(
        n.properties.get("action").and_then(|v| v.as_str()),
        Some("write_note")
    );
    let head = g
        .branch_head(&ctx, NAMESPACE_METHODOLOGY, MAIN_BRANCH)
        .await
        .expect("head");
    let (_, links) = g.baseline_graph(&ctx, &head.id).await.expect("graph");
    assert!(
        has_link(
            &*g,
            &links,
            LINK_SUB_ACTIVITY,
            &format!("{hk}/method/inspect"),
            &format!("{hk}/methodStep/inspect/note")
        )
        .await,
        "method -> method step sub_activity link missing"
    );
    let got = s.get(&ctx, &m.name, &m.version).await.expect("get");
    assert_eq!(canon(&got.methodology.methods), canon(&m.methods));
}

// Saving a methodology again, with a change to one element, only touches what changed.
#[tokio::test]
async fn graph_store_resave_with_steps_changes_only_what_changed() {
    graph_store_resave_with_steps_changes_only_what_changed_over(FakeGraph::typed()).await;
    graph_store_resave_with_steps_changes_only_what_changed_over(real()).await;
}

async fn graph_store_resave_with_steps_changes_only_what_changed_over<G: Probe + 'static>(
    g: Arc<G>,
) {
    let ctx = bg();
    let s = GraphStore::new(g);
    let now = at(2026, 9, 26, 10, 0);
    let m = goap_methodology::load_file("../../methodologies/sdlc.yaml").expect("sdlc");
    s.save(&ctx, draft(&m, Some(now), now)).await.expect("save");
    s.save(&ctx, draft(&m, Some(now), now))
        .await
        .expect("saving it again unchanged");
    let mut changed = m.clone();
    for c in &mut changed.conditions {
        if c.name == "functions_allocated" {
            c.expr.push_str(" && true");
        }
    }
    s.save(
        &ctx,
        draft(&changed, Some(now), now + chrono::Duration::minutes(1)),
    )
    .await
    .expect("saving it with a changed condition");
    let got = s.get(&ctx, &m.name, &m.version).await.expect("get");
    assert_eq!(canon(&got.methodology), canon(&changed));
}
