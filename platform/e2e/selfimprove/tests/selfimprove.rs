//! The self-observation builtins on a real graph (ADR 0125 §5: the engine's side does not import the graph).

use std::collections::BTreeMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_authz::{self as authz, Principal};
use goap_ctx::Ctx;
use goap_def::Issues;
use goap_domain::{self as domain, BaselineID, REVIEW_PROPOSED};
use goap_dsl::NodeOp;
use goap_engine::{
    AuthzScope, BuiltinExecutor, Engine, HumanExecutor, ItemInput, LLMExecutor, MemoryStore,
    STATUS_COMPLETED, STATUS_WAITING, StartRequest, StaticMethodologies, Store, default_builtins,
};
use goap_graph::{Commit, CommitResult, Graph, LinkEdit, LinkWrite, Memory, NodeEdit};
use goap_graphtest as graphtest;
use goap_llm as llm;
use goap_methodology::{self as methodology, Compiled, Methodology};
use goap_observe::{self as observe, Thresholds};
use goap_selfimprove::{self as selfimprove, BoxError, MethodologyDrafts};
use parking_lot::Mutex;
use serde_json::{Map, Value, json};

/// MemDrafts is an in-memory registry of definitions.
#[derive(Default)]
struct MemDrafts {
    inner: Mutex<DraftsState>,
}

#[derive(Default)]
struct DraftsState {
    defs: BTreeMap<String, Methodology>, // name@version
    saved: Vec<Methodology>,
    who: Vec<Principal>,
}

#[async_trait]
impl MethodologyDrafts for MemDrafts {
    async fn definition(
        &self,
        _ctx: &Ctx,
        name: &str,
        version: &str,
    ) -> Result<Option<Methodology>, BoxError> {
        let version = if version.is_empty() { "1.2.0" } else { version };
        Ok(self
            .inner
            .lock()
            .defs
            .get(&format!("{name}@{version}"))
            .cloned())
    }

    async fn save_draft(&self, ctx: &Ctx, m: Methodology) -> Result<Issues, BoxError> {
        let mut st = self.inner.lock();
        st.saved.push(m.clone());
        st.who.push(authz::principal_from(ctx));
        st.defs
            .insert(format!("{}@{}", m.name, m.version), m.clone());
        Ok(m.validate())
    }
}

fn obj(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

/// store_definition writes the definition nodes of a methodology version as the registry does (ADR 0023): the
/// version node and one node per element, typed by the meta-domain methodology, the actions of an agent its uses
/// links (ADR 0120).
async fn store_definition(
    ctx: &Ctx,
    g: &Graph,
    base: BaselineID,
    m: &Methodology,
) -> goap_graph::Result<CommitResult> {
    let hkey = observe::element_key(&m.name, &m.version, "methodology", "");
    let mut head = NodeEdit {
        key: hkey,
        r#type: "methodology@MethodologyVersion".into(),
        props: obj(json!({"name": m.name, "version": m.version, "status": "published"})),
        ..Default::default()
    };
    let mut edits = Vec::new();
    let mut el =
        |kind: &str, typ: &str, name: &str, mut props: Map<String, Value>, links: Vec<LinkEdit>| {
            let k = observe::element_key(&m.name, &m.version, kind, name);
            props.insert("name".into(), Value::from(name));
            edits.push(NodeEdit {
                key: k.clone(),
                r#type: typ.into(),
                props,
                links,
                ..Default::default()
            });
            head.links.push(LinkEdit {
                r#type: "methodology@defines".into(),
                to_key: k,
                ..Default::default()
            });
        };
    for a in &m.actions {
        el(
            "action",
            "methodology@Action",
            &a.name,
            obj(json!({"kind": a.kind, "cost": a.cost, "model": a.model})),
            vec![],
        );
    }
    for a in &m.agents {
        let uses = a
            .actions
            .iter()
            .enumerate()
            .map(|(i, x)| LinkEdit {
                r#type: methodology::LINK_USES.into(),
                to_key: observe::element_key(&m.name, &m.version, "action", x),
                props: obj(json!({"position": i as f64})),
                ..Default::default()
            })
            .collect();
        el("agent", "methodology@Agent", &a.name, Map::new(), uses);
    }
    edits.push(head);
    Box::pin(g.commit(
        ctx,
        Commit {
            project_id: "PROJ-ROOT".into(),
            namespace: "methodology".into(),
            title: format!("Methodology {}", m.name),
            baseline: base,
            by: "test".into(),
            edits,
            ..Default::default()
        },
    ))
    .await
}

const TEST_PROJECT: &str = "PROJ-TEST";

/// scripted answers by prompt content, like a deterministic LLM. The matched substrings come from the prompt
/// templates in methodologies/examples/impact-analysis.yaml. Unexpected prompts are recorded in errors.
fn scripted(errors: Arc<Mutex<Vec<String>>>) -> Arc<dyn llm::Client> {
    llm::ClientFunc::shared(move |_ctx: Ctx, req: llm::Request| {
        let errors = errors.clone();
        async move {
            let p = req.messages[0].content.clone();
            let out = if p.contains("DIRECTLY impacted") {
                if !p.contains("REQ-1 (alm@Requirement)") {
                    errors
                        .lock()
                        .push(format!("prompt misses baseline nodes:\n{p}"));
                }
                r#"Here: {"items":[{"type":"changeImpact","changeImpact":{"op":"declare","intent":"modified","key":"REQ-1","rationale":"PSP API change"}}]}"#
            } else if p.contains("new version") {
                r#"{"items":[{"type":"changeImpact","changeImpact":{"op":"write","node":"REQ-1","props":{"title":"Use PSP v2"}}}]}"#
            } else if p.contains("test case") {
                r##"{"items":[{"type":"changeImpact","changeImpact":{"op":"declare","ref":"#t1","intent":"created","type":"TestCase","key":"TST-9","rationale":"cover REQ-1"}},
			{"type":"changeImpact","changeImpact":{"op":"write","node":"#t1","props":{"title":"PSP v2 test"},"links":[{"type":"verifies","to":"REQ-1"}]}}]}"##
            } else if p.contains("report") {
                r##"{"items":[{"type":"artifact","target":"report","data":{"markdown":"# Impact"}}]}"##
            } else {
                errors.lock().push(format!("unexpected prompt {p}"));
                ""
            };
            Ok(llm::Response {
                text: out.to_string(),
                provider: "test".into(),
                model: req.model.clone(),
                ..Default::default()
            })
        }
    })
}

fn path(rel: &str) -> String {
    format!("{}/../../../{rel}", env!("CARGO_MANIFEST_DIR"))
}

fn props(v: Value) -> Map<String, Value> {
    obj(v)
}

struct Setup {
    graph: Arc<Graph>,
    store: Arc<MemoryStore>,
    client: Arc<dyn llm::Client>,
    impact: Arc<Compiled>,
    base: BaselineID,
}

async fn setup(errors: Arc<Mutex<Vec<String>>>) -> Setup {
    let ctx = Ctx::background();
    let m = methodology::load_file(path("methodologies/examples/impact-analysis.yaml")).unwrap();
    let cm = Arc::new(m.compile().unwrap());
    let g = Arc::new(Graph::new(Arc::new(Memory::new())));
    // a project every test starts its (non-administrative) processes in (ADR 0039)
    graphtest::project(&ctx, &g, TEST_PROJECT, "Test")
        .await
        .unwrap();
    // the alm namespace the example methodologies act on (ADR 0013)
    let node =
        |key: &str, typ: &str, p: Map<String, Value>, links: Vec<LinkWrite>| graphtest::Node {
            namespace: "alm".into(),
            key: key.into(),
            r#type: typ.into(),
            properties: p,
            links,
            ..Default::default()
        };
    let link = |typ: &str, to: &domain::Node| LinkWrite {
        r#type: typ.into(),
        to: to.r#ref(),
        ..Default::default()
    };
    let need = graphtest::import(
        &ctx,
        &g,
        node(
            "NEED-1",
            "alm@Need",
            props(json!({"title": "Pay online"})),
            vec![],
        ),
    )
    .await
    .unwrap();
    let req = graphtest::import(
        &ctx,
        &g,
        node(
            "REQ-1",
            "alm@Requirement",
            props(json!({"title": "Use PSP v1"})),
            vec![link("alm@satisfies", &need)],
        ),
    )
    .await
    .unwrap();
    graphtest::import(
        &ctx,
        &g,
        node(
            "TST-1",
            "alm@TestCase",
            Map::new(),
            vec![link("alm@verifies", &req)],
        ),
    )
    .await
    .unwrap();
    graphtest::import(
        &ctx,
        &g,
        node(
            "CMP-1",
            "alm@Component",
            Map::new(),
            vec![link("alm@implements", &req)],
        ),
    )
    .await
    .unwrap();
    let b = g
        .branch_head(&ctx, "alm", domain::MAIN_BRANCH)
        .await
        .unwrap();
    Setup {
        graph: g,
        store: Arc::new(MemoryStore::new()),
        client: scripted(errors),
        impact: cm,
        base: b.id,
    }
}

/// engine builds an engine over the setup with the methodologies given and its builtins (Go mutated the engine's
/// methodologies and its builtin table in place; here a second engine over the same graph and store is built).
fn engine(
    s: &Setup,
    methodologies: Vec<Arc<Compiled>>,
    register: impl FnOnce(&mut BuiltinExecutor, &Engine),
) -> Arc<Engine> {
    let mut ms = StaticMethodologies::new();
    for m in methodologies {
        ms.0.insert(m.name().to_string(), m);
    }
    let store: Arc<dyn Store> = s.store.clone();
    let mut e = Engine::new(s.graph.clone(), Arc::new(ms), store);
    e.intent = Some(goap_intent::Resolver::new(Arc::new(goap_intent::Lexical)));
    e.scope = Some(Arc::new(AuthzScope {
        authz: Some(Arc::new(authz::Casbin::new().unwrap())),
        hub: None,
    }));
    let mut b = default_builtins();
    register(&mut b, &e);
    e.executors.insert(
        methodology::KIND_LLM.into(),
        Arc::new(LLMExecutor {
            client: Some(s.client.clone()),
        }),
    );
    e.executors
        .insert(methodology::KIND_HUMAN.into(), Arc::new(HumanExecutor));
    e.executors
        .insert(methodology::KIND_BUILTIN.into(), Arc::new(b));
    Arc::new(e)
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn self_observation_proposes_and_drafts() {
    // drafting a methodology is administration (ADR 0043)
    let ctx = authz::with_principal(
        &Ctx::background(),
        Principal {
            subject: "mia".into(),
            org: "acme".into(),
            roles: vec!["admin".into()],
            ..Default::default()
        },
    );
    let errors = Arc::new(Mutex::new(Vec::new()));
    let s = setup(errors.clone()).await;
    let e = engine(&s, vec![s.impact.clone()], |_, _| {});
    // the observed run; the intent text deliberately echoes the "assess_impact" goal example in
    // methodologies/examples/impact-analysis.yaml.
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                baseline_id: s.base.clone(),
                intent: "The PSP changes its API, what does this break?".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert_eq!(p.status.as_str(), STATUS_COMPLETED, "observed run: {p:?}");
    // the observer methodology, and the definition nodes of the observed one in the graph
    let obs_m = methodology::load_file(path("methodologies/methodology-improvement.yaml")).unwrap();
    let obs = Arc::new(obs_m.compile().unwrap());
    // the definition nodes live in the methodology namespace, not the observed run's (alm)
    let g = s.graph.clone();
    let mbase = g
        .branch_head(&ctx, "methodology", domain::MAIN_BRANCH)
        .await
        .unwrap();
    let res = store_definition(&ctx, &g, mbase.id, s.impact.definition())
        .await
        .expect("definition nodes");
    let drafts = Arc::new(MemDrafts::default());
    drafts.inner.lock().defs.insert(
        "impact-analysis@1.2.0".into(),
        s.impact.definition().clone(),
    );
    let d2: Arc<dyn MethodologyDrafts> = drafts.clone();
    let e = engine(&s, vec![s.impact.clone(), obs.clone()], |b, e| {
        selfimprove::register(
            b,
            e,
            selfimprove::Config {
                drafts: Some(d2),
                thresholds: Thresholds {
                    llm_calls: 1,
                    min_systematized: 1,
                    ..Default::default()
                },
                ..Default::default()
            },
        )
    });

    let op = e
        .start(
            &ctx,
            StartRequest {
                methodology: obs.name().to_string(),
                agent: "observer".into(),
                goal: "improve_methodology".into(),
                baseline_id: res.baseline.id.clone(),
                intent: "observe".into(),
                vars: obj(json!({"event": {"process": {"id": p.id}}})),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let op = e.run(&ctx, &op.id).await.unwrap();
    assert!(
        op.status.as_str() == STATUS_WAITING
            && op
                .pending
                .as_ref()
                .is_some_and(|t| t.action == "review_improvements"),
        "observer must wait for the review: {op:?}"
    );
    let st = &op.steps[1];
    assert!(
        st.action == "propose_improvements" && st.specialization == "propose_by_rules",
        "rules specialization expected: {st:?}"
    );
    let c = g.change(&ctx, &op.change_id).await.unwrap();
    let mut report = Map::new();
    for it in &c.items {
        if it.target == "cost_report" {
            report = it.data.clone();
        }
    }
    // every improvement is a change impact on an element of the methodology, with the proposed version written
    let mut decisions = Vec::new();
    let mut titles = Vec::new();
    for n in &c.nodes {
        titles.push(n.rationale.clone());
        assert!(
            n.post.is_some() && n.review == REVIEW_PROPOSED,
            "an improvement is written and awaits its review: {n:?}"
        );
        decisions.push(ItemInput {
            r#type: "changeImpact".into(),
            change_impact: Some(NodeOp {
                op: "review".into(),
                node: n.key.clone(),
                accept: true,
                comment: "worth it".into(),
                ..Default::default()
            }),
            ..Default::default()
        });
    }
    // the specialization proposed is a link to the action it specializes, not a name in its properties (ADR 0120)
    let mut specialized = String::new();
    for n in &c.nodes {
        if !n.key.ends_with("/action/identify_impacts_script") {
            continue;
        }
        let v = g
            .change_node_view(&ctx, &c.id, "", n.post.as_ref().unwrap())
            .await
            .unwrap();
        assert!(
            !v.node.properties.contains_key("specializes"),
            "the specialization is a link, not a property: {:?}",
            v.node.properties
        );
        for l in &v.out {
            if l.r#type == methodology::LINK_SPECIALIZES {
                let to = g.node(&ctx, &l.to).await.unwrap();
                specialized = to.key.clone();
            }
        }
    }
    assert!(
        specialized.ends_with("/action/identify_impacts"),
        "specializes link of the proposed script: {specialized:?}"
    );
    let findings = report
        .get("findings")
        .and_then(Value::as_array)
        .map_or(0, Vec::len);
    assert!(
        findings >= 2 && report.get("methodology") == Some(&Value::from("impact-analysis")),
        "report: {report:?}"
    );
    let joined = titles.join(" | ");
    assert!(
        joined.contains("Specialize identify_impacts with a script"),
        "proposals: {joined}"
    );
    e.submit(&ctx, &op.id, decisions).await.unwrap();
    let op = e.run(&ctx, &op.id).await.unwrap();
    assert_eq!(op.status.as_str(), STATUS_COMPLETED, "observer end: {op:?}");
    let (saved, who) = {
        let st = drafts.inner.lock();
        (st.saved.clone(), st.who.clone())
    };
    assert_eq!(saved.len(), 1, "drafts: {saved:?}");
    let d = &saved[0];
    assert!(
        d.version == "1.2.1" && who[0].subject == "mia",
        "draft version {} by {:?}",
        d.version,
        who[0]
    );
    let spec = d
        .actions
        .iter()
        .find(|a| a.name == "identify_impacts_script");
    assert!(
        spec.is_some_and(|a| a.specializes == "identify_impacts"
            && a.kind == "script"
            && a.code.contains("function run(ctx)")),
        "specialization not drafted: {:?}",
        d.actions
    );
    let is = d.validate();
    assert!(is.0.is_empty(), "draft must be valid: {is:?}");
    // the observer itself is journaled on its change
    let recs = goap_journal::read(
        &ctx,
        &*g,
        &goap_journal::Filter {
            change_id: op.change_id.clone(),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert!(
        recs.last()
            .is_some_and(|r| r.kind == goap_journal::KIND_PROCESS_ENDED),
        "observer journal: {recs:?}"
    );
    let errs = errors.lock();
    assert!(errs.is_empty(), "{errs:?}");
}
