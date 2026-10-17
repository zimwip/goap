//! Unit tests of the built-in connectors over fakes of their ports: the dispatch of the operations, the decoding of
//! their arguments and the gates. The tests over a real graph, engine and hub are platform/e2e/builtin's.

use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::Arc;

use chrono::{TimeZone, Utc};
use goap_authz::{self as authz, Principal};
use goap_connect::{ConnectError, Request, Response};
use goap_connectorkit::Connector;
use goap_ctx::Ctx;
use goap_domain::{
    self as domain, Baseline, BaselineID, Blackboard, BoardIssue, Branch, ChangeID, ChangeImpact,
    ChangeImpactID, ChangeItem, DecisionPoint, Flow, Link, LinkID, LogEntry, LogFilter, Node,
    NodeID, NodeRef, NodeReview, ReviewBatch, Structures,
};
use goap_gen::engine::v1 as enginev1;
use goap_graphapi::{self as graphapi, Error as GErr, Port};
use goap_mcpbuiltin as mcpbuiltin;
use parking_lot_free::Mutex;
use serde_json::{Map, Value, json};

use crate::builtin::{Args, DEFAULT_LIMIT, schema};
use crate::{Error, Ports, connectors};

/// A std Mutex under the name the tests use (the crate does not depend on parking_lot).
mod parking_lot_free {
    #[derive(Default)]
    pub struct Mutex<T>(std::sync::Mutex<T>);
    impl<T> Mutex<T> {
        pub fn lock(&self) -> std::sync::MutexGuard<'_, T> {
            self.0.lock().unwrap()
        }
    }
}

fn unsupported<T>() -> Result<T, GErr> {
    Err(GErr::msg("unsupported by the fake graph"))
}

/// FakeGraph is a graph of one baseline per namespace, with the calls the connectors made.
#[derive(Default)]
struct FakeGraph {
    nodes: Vec<Node>,
    links: Vec<Link>,
    baselines: Vec<Baseline>,
    admin_only: HashSet<String>,
    board: Blackboard,
    calls: Mutex<Vec<String>>,
    created: Mutex<Vec<graphapi::NodeCreate>>,
    withdrawn: Mutex<Vec<Vec<ChangeImpactID>>>,
    items: Mutex<Vec<ChangeItem>>,
}

impl FakeGraph {
    fn call(&self, c: &str) {
        self.calls.lock().push(c.to_string());
    }
    fn calls(&self) -> Vec<String> {
        self.calls.lock().clone()
    }
}

#[async_trait::async_trait]
impl Port for FakeGraph {
    async fn create_change(
        &self,
        _: &Ctx,
        in_: graphapi::NewChange,
    ) -> Result<domain::Change, GErr> {
        self.call("create_change");
        Ok(domain::Change {
            id: ChangeID::from("CH-NEW"),
            title: in_.title,
            intent: in_.intent,
            namespace: in_.namespace,
            baseline_id: in_.baseline_id,
            owner_org: in_.owner_org,
            project_id: in_.project_id,
            data: in_.data,
            ..Default::default()
        })
    }
    async fn update_change(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: graphapi::ChangePatch,
    ) -> Result<domain::Change, GErr> {
        unsupported()
    }
    async fn move_change(&self, _: &Ctx, _: &ChangeID, _: &str) -> Result<domain::Change, GErr> {
        unsupported()
    }
    async fn attach_change(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &ChangeID,
    ) -> Result<domain::Change, GErr> {
        unsupported()
    }
    async fn detach_change(&self, _: &Ctx, _: &ChangeID) -> Result<domain::Change, GErr> {
        unsupported()
    }
    async fn set_change_dependencies(
        &self,
        _: &Ctx,
        id: &ChangeID,
        deps: Vec<ChangeID>,
    ) -> Result<domain::Change, GErr> {
        Ok(domain::Change {
            id: id.clone(),
            depends_on: deps,
            ..Default::default()
        })
    }
    async fn change_dependencies(
        &self,
        _: &Ctx,
        _: &ChangeID,
    ) -> Result<graphapi::ChangeDependencies, GErr> {
        unsupported()
    }
    async fn upgrade_change(&self, _: &Ctx, _: &ChangeID, _: &str) -> Result<domain::Change, GErr> {
        unsupported()
    }
    async fn change(&self, _: &Ctx, _: &ChangeID) -> Result<domain::Change, GErr> {
        Ok(self.board.change.clone())
    }
    async fn add_items(
        &self,
        _: &Ctx,
        _: &ChangeID,
        items: Vec<ChangeItem>,
    ) -> Result<Vec<ChangeItem>, GErr> {
        self.items.lock().extend(items.clone());
        Ok(items)
    }
    async fn propose_impact(
        &self,
        _: &Ctx,
        _: &ChangeID,
        nodes: Vec<ChangeImpact>,
    ) -> Result<Vec<ChangeImpact>, GErr> {
        Ok(nodes)
    }
    async fn impact_node_create(
        &self,
        _: &Ctx,
        _: &ChangeID,
        in_: graphapi::NodeCreate,
    ) -> Result<ChangeImpact, GErr> {
        self.created.lock().push(in_.clone());
        Ok(ChangeImpact {
            id: ChangeImpactID::from("CI-1"),
            key: in_.key,
            r#type: in_.r#type,
            intent: domain::NodeIntent::from("created"),
            ..Default::default()
        })
    }
    async fn impact_node_checkout(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: graphapi::NodeCheckout,
    ) -> Result<ChangeImpact, GErr> {
        unsupported()
    }
    async fn impact_node_update(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &ChangeImpactID,
        _: graphapi::NodeUpdate,
    ) -> Result<ChangeImpact, GErr> {
        unsupported()
    }
    async fn impact_link_create(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &ChangeImpactID,
        _: graphapi::LinkWrite,
        _: &str,
        _: &str,
    ) -> Result<Link, GErr> {
        unsupported()
    }
    async fn impact_link_delete(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &LinkID,
        _: &str,
        _: &str,
    ) -> Result<(), GErr> {
        unsupported()
    }
    async fn impact_node_transition(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: graphapi::NodeTransition,
    ) -> Result<ChangeImpact, GErr> {
        unsupported()
    }
    async fn impact_node_cancel(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &ChangeImpactID,
        _: &str,
        _: &str,
    ) -> Result<ChangeImpact, GErr> {
        unsupported()
    }
    async fn withdraw_impact(
        &self,
        _: &Ctx,
        _: &ChangeID,
        impact: &ChangeImpactID,
        _: &str,
        _: &str,
    ) -> Result<(), GErr> {
        self.withdrawn.lock().push(vec![impact.clone()]);
        Ok(())
    }
    async fn withdraw_impacts(
        &self,
        _: &Ctx,
        _: &ChangeID,
        impacts: Vec<ChangeImpactID>,
        _: &str,
        _: &str,
    ) -> Result<(), GErr> {
        self.withdrawn.lock().push(impacts);
        Ok(())
    }
    async fn impact_node_merge(
        &self,
        _: &Ctx,
        _: &ChangeID,
        in_: graphapi::MergeInput,
    ) -> Result<graphapi::Restructured, GErr> {
        // the parents the fake finds: every node linking to a source
        let gate = in_.gate.expect("a merge carries the gate");
        for n in &self.nodes {
            gate(&n.r#type)?;
        }
        Ok(graphapi::Restructured::default())
    }
    async fn impact_node_split(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: graphapi::SplitInput,
    ) -> Result<graphapi::Restructured, GErr> {
        unsupported()
    }
    async fn impact_node_review_on(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &str,
        _: &str,
        _: &ChangeImpactID,
        _: NodeReview,
        _: &str,
        _: &str,
    ) -> Result<ChangeImpact, GErr> {
        unsupported()
    }
    async fn impact_node_review_batch(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: ReviewBatch,
    ) -> Result<Vec<ChangeImpact>, GErr> {
        unsupported()
    }
    async fn reopen_impacts(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: Vec<ChangeImpactID>,
        _: &str,
    ) -> Result<Vec<ChangeImpactID>, GErr> {
        unsupported()
    }
    async fn blackboard(&self, _: &Ctx, _: &ChangeID) -> Result<Blackboard, GErr> {
        Ok(self.board.clone())
    }
    async fn blackboard_in(&self, _: &Ctx, _: &ChangeID, _: &str) -> Result<Blackboard, GErr> {
        Ok(self.board.clone())
    }
    async fn open_flow(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: graphapi::OpenFlowRequest,
    ) -> Result<Flow, GErr> {
        unsupported()
    }
    async fn adopt_flow(&self, _: &Ctx, _: &ChangeID, _: &str, _: &str) -> Result<Flow, GErr> {
        unsupported()
    }
    async fn discard_flow(&self, _: &Ctx, _: &ChangeID, _: &str, _: &str) -> Result<Flow, GErr> {
        unsupported()
    }
    async fn validate_board(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &str,
    ) -> Result<Vec<BoardIssue>, GErr> {
        Ok(Vec::new())
    }
    async fn baseline_graph(
        &self,
        _: &Ctx,
        _: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>), GErr> {
        self.call("baseline_graph");
        Ok((self.nodes.clone(), self.links.clone()))
    }
    async fn change_graph(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &str,
    ) -> Result<(Vec<Node>, Vec<Link>), GErr> {
        Ok((self.nodes.clone(), self.links.clone()))
    }
    async fn change_view(&self, _: &Ctx, _: &ChangeID, _: &str, _: &str) -> Result<Baseline, GErr> {
        unsupported()
    }
    async fn open_option(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: graphapi::OpenOptionRequest,
    ) -> Result<Flow, GErr> {
        unsupported()
    }
    async fn activate_option(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &str,
        _: &str,
    ) -> Result<String, GErr> {
        Ok(String::new())
    }
    async fn evaluate_option(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &str,
        _: &str,
        _: &str,
    ) -> Result<Flow, GErr> {
        unsupported()
    }
    async fn options(&self, _: &Ctx, _: &ChangeID) -> Result<Vec<Flow>, GErr> {
        Ok(Vec::new())
    }
    async fn compare_options(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &str,
        _: bool,
    ) -> Result<graphapi::OptionComparison, GErr> {
        unsupported()
    }
    async fn open_decision(
        &self,
        _: &Ctx,
        _: &ChangeID,
        in_: graphapi::OpenDecisionRequest,
    ) -> Result<DecisionPoint, GErr> {
        // the policy handed over, as the point's question (the fake keeps it to be read back)
        self.call(&format!(
            "open_decision {} {:?} {}",
            in_.question,
            in_.options,
            Value::Object(in_.policy)
        ));
        unsupported()
    }
    async fn rule_decision(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: graphapi::RuleRequest,
    ) -> Result<DecisionPoint, GErr> {
        unsupported()
    }
    async fn answer_question(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &str,
        _: &str,
        _: &str,
        _: &str,
    ) -> Result<DecisionPoint, GErr> {
        unsupported()
    }
    async fn ratify_decision(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: &str,
        _: bool,
        _: &str,
        _: &str,
    ) -> Result<DecisionPoint, GErr> {
        unsupported()
    }
    async fn decision_points(&self, _: &Ctx, _: &ChangeID) -> Result<Vec<DecisionPoint>, GErr> {
        Ok(Vec::new())
    }
    async fn apply(&self, _: &Ctx, _: &ChangeID, _: &str) -> Result<Baseline, GErr> {
        unsupported()
    }
    async fn branch(&self, _: &Ctx, _: &str, _: &str) -> Result<Branch, GErr> {
        unsupported()
    }
    async fn plan_merge(
        &self,
        _: &Ctx,
        _: &str,
        _: &str,
        _: &str,
    ) -> Result<graphapi::MergePlan, GErr> {
        unsupported()
    }
    async fn integrate_change(
        &self,
        _: &Ctx,
        _: &ChangeID,
        _: BTreeMap<NodeID, graphapi::Resolution>,
    ) -> Result<domain::Change, GErr> {
        unsupported()
    }
    async fn rebase_state(&self, _: &Ctx, _: &ChangeID) -> Result<graphapi::RebaseState, GErr> {
        unsupported()
    }
    async fn baselines(&self, _: &Ctx, _: &str) -> Result<Vec<Baseline>, GErr> {
        Ok(self.baselines.clone())
    }
    async fn append_log(&self, _: &Ctx, _: Vec<LogEntry>) -> Result<(), GErr> {
        unsupported()
    }
    async fn change_log(
        &self,
        _: &Ctx,
        _: LogFilter,
    ) -> Result<(Vec<LogEntry>, BTreeMap<String, i64>), GErr> {
        unsupported()
    }
    async fn branch_head(&self, _: &Ctx, namespace: &str, _: &str) -> Result<Baseline, GErr> {
        if namespace == "missing" {
            return Err(GErr::not_found("no such namespace"));
        }
        Ok(Baseline {
            id: BaselineID::from(format!("BL-{namespace}")),
            ..Default::default()
        })
    }
    async fn list_changes(
        &self,
        _: &Ctx,
        _: graphapi::ChangesFilter,
    ) -> Result<Vec<domain::Change>, GErr> {
        let at = |s| Utc.timestamp_opt(s, 0).unwrap();
        Ok((1..=3)
            .map(|i| domain::Change {
                id: ChangeID::from(format!("CH-{i}")),
                created_at: at(i),
                ..Default::default()
            })
            .collect())
    }
    async fn structures(&self, _: &Ctx) -> Result<Structures, GErr> {
        Ok(Structures::default())
    }
    async fn admin_only_type(&self, _: &Ctx, typ: &str) -> Result<bool, GErr> {
        Ok(self.admin_only.contains(typ))
    }
}

/// Deny refuses the requests whose resource type it names.
struct Deny(Vec<&'static str>);

#[async_trait::async_trait]
impl authz::Authorizer for Deny {
    async fn authorize(&self, _: &Ctx, req: &authz::Request) -> Result<bool, authz::Error> {
        Ok(!self.0.contains(&req.resource.r#type.as_str()))
    }
}

/// FakeEngine records the requests it served.
#[derive(Default)]
struct FakeEngine {
    started: Mutex<Vec<Request<enginev1::StartProcessRequest>>>,
}

#[goap_connect::async_trait]
impl enginev1::EngineService for FakeEngine {
    async fn start_process(
        &self,
        _: &Ctx,
        req: Request<enginev1::StartProcessRequest>,
    ) -> Result<Response<enginev1::StartProcessResponse>, ConnectError> {
        let msg = req.msg.clone();
        self.started.lock().push(req);
        Ok(Response::new(enginev1::StartProcessResponse {
            process: Some(enginev1::Process {
                id: "P-1".into(),
                goal: msg.goal,
                steps: vec![enginev1::Step::default()],
                world: HashMap::from([("x".to_string(), true)]),
                ..Default::default()
            }),
        }))
    }

    async fn list_processes(
        &self,
        _: &Ctx,
        _: Request<enginev1::ListProcessesRequest>,
    ) -> Result<Response<enginev1::ListProcessesResponse>, ConnectError> {
        let p = |id: &str, s: i64| enginev1::Process {
            id: id.into(),
            created_at: Some(pbjson_types::Timestamp {
                seconds: s,
                nanos: 0,
            }),
            steps: vec![enginev1::Step::default()],
            ..Default::default()
        };
        Ok(Response::new(enginev1::ListProcessesResponse {
            processes: vec![p("old", 1), p("new", 3), p("mid", 2)],
        }))
    }

    async fn get_process(
        &self,
        _: &Ctx,
        _: Request<enginev1::GetProcessRequest>,
    ) -> Result<Response<enginev1::GetProcessResponse>, ConnectError> {
        Ok(Response::new(enginev1::GetProcessResponse::default()))
    }
}

fn as_user(subject: &str) -> Ctx {
    authz::with_principal(
        &Ctx::background(),
        Principal {
            subject: subject.into(),
            org: "ORG-A".into(),
            project: "PROJ-A".into(),
            ..Default::default()
        },
    )
}

fn args(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

fn node(ns: &str, key: &str, typ: &str, props: Value) -> Node {
    Node {
        id: NodeID::from(format!("N-{key}")),
        version: 1,
        namespace: ns.into(),
        key: key.into(),
        r#type: typ.into(),
        properties: args(props),
        ..Default::default()
    }
}

fn link(from: &Node, typ: &str, to: &Node) -> Link {
    Link {
        id: LinkID::from(format!("L-{}-{}", from.key, to.key)),
        r#type: typ.into(),
        from: from.r#ref(),
        to: to.r#ref(),
        ..Default::default()
    }
}

fn sample_graph() -> FakeGraph {
    let a = node(
        "alm",
        "REQ-1",
        "alm@Requirement",
        json!({"title": "Login", "text": "users sign in"}),
    );
    let b = node(
        "alm",
        "REQ-2",
        "alm@Requirement",
        json!({"title": "Logout"}),
    );
    let s = node("alm", "SEC-1", "alm@Secret", json!({"title": "hidden"}));
    let o = node("other", "X-1", "other@Thing", json!({}));
    let links = vec![
        link(&a, "alm@depends_on", &b),
        link(&a, "alm@uses", &s),
        link(&b, "alm@ref", &o),
    ];
    FakeGraph {
        nodes: vec![b, a, s, o],
        links,
        ..Default::default()
    }
}

fn ports(g: Arc<FakeGraph>) -> Ports {
    Ports {
        graph: Some(g),
        ..Default::default()
    }
}

#[test]
fn connectors_follow_the_ports() {
    let only_graph = connectors(ports(Arc::new(FakeGraph::default())));
    let mut ids: Vec<&String> = only_graph.keys().collect();
    ids.sort();
    assert_eq!(ids, [mcpbuiltin::CHANGE, mcpbuiltin::GRAPH]);
    assert!(connectors(Ports::default()).is_empty());

    let eng: Arc<crate::EngineAPI> = Arc::new(FakeEngine::default());
    let p = Ports {
        engine: Some(eng),
        ..Default::default()
    };
    let cs = connectors(p);
    assert_eq!(cs.len(), 1);
    let info = cs[mcpbuiltin::SCHEDULER].info();
    assert_eq!(info.version, crate::VERSION);
    let names: Vec<&str> = info.operations.iter().map(|o| o.name.as_str()).collect();
    assert_eq!(
        names,
        [
            "start",
            "attach",
            "list",
            "get",
            "starting_points",
            "triggers",
            "fire"
        ]
    );
}

#[test]
fn infos_describe_every_operation() {
    let cs = connectors(ports(Arc::new(FakeGraph::default())));
    let change = cs[mcpbuiltin::CHANGE].info();
    assert_eq!(change.id, mcpbuiltin::CHANGE);
    assert_eq!(change.operations.len(), 42);
    let write = change
        .operations
        .iter()
        .find(|o| o.name == "write")
        .unwrap();
    let s = goap_pbconv::to_map(write.input_schema.as_ref());
    assert_eq!(s["type"], "object");
    assert_eq!(s["required"], json!(["key", "type", "rationale"]));
    assert_eq!(s["properties"]["properties"], json!({"type": "object"}));
    let graph = cs[mcpbuiltin::GRAPH].info();
    assert_eq!(graph.operations.len(), 5);
    // an operation with no property still has a schema
    let admin = crate::Admin::new(Ports::default()).info();
    assert_eq!(admin.operations.len(), 6);
    assert!(admin.operations.iter().all(|o| o.input_schema.is_some()));
}

#[test]
fn schema_lists_typed_properties_and_required_ones() {
    let s = schema(&[("a", "string"), ("n", "integer")], &["a"]);
    assert_eq!(
        Value::Object(s),
        json!({"type": "object", "properties": {"a": {"type": "string"}, "n": {"type": "integer"}}, "required": ["a"]})
    );
    assert!(!schema(&[], &[]).contains_key("required"));
}

#[test]
fn args_decode_like_go() {
    let m = args(json!({"s": "  x ", "n": 3, "b": true, "o": {"k": 1}, "limit": 2.9, "bad": 1}));
    let a = Args(&m);
    assert_eq!(a.str("s"), "x");
    assert_eq!(a.str("n"), "");
    assert!(a.boolean("b"));
    assert!(!a.boolean("s"));
    assert_eq!(a.object("o")["k"], 1);
    assert!(a.object("s").is_empty());
    assert_eq!(a.limit(), 2);
    assert_eq!(
        a.required("missing").unwrap_err().to_string(),
        r#"argument "missing" is required"#
    );
    let none = Map::new();
    assert_eq!(Args(&none).limit(), DEFAULT_LIMIT);
    let neg = args(json!({"limit": -1}));
    assert_eq!(Args(&neg).limit(), DEFAULT_LIMIT);

    // the unit: named, else the call's, else the principal's
    let p = Principal {
        subject: "u".into(),
        org: "ORG-P".into(),
        ..Default::default()
    };
    let ctx = Ctx::background();
    assert_eq!(Args(&none).unit(&ctx, &p), "ORG-P");
    let call = goap_mcp::with_call(
        &ctx,
        goap_mcp::CallContext {
            unit: "ORG-CALL".into(),
            ..Default::default()
        },
    );
    assert_eq!(Args(&none).unit(&call, &p), "ORG-CALL");
    let named = args(json!({"unit": "ORG-N"}));
    assert_eq!(Args(&named).unit(&call, &p), "ORG-N");
}

#[tokio::test]
async fn anonymous_calls_are_refused() {
    let g = Arc::new(sample_graph());
    let cs = connectors(ports(g));
    for (id, op) in [(mcpbuiltin::GRAPH, "read"), (mcpbuiltin::CHANGE, "read")] {
        let err = cs[id]
            .invoke(
                &Ctx::background(),
                op,
                args(json!({"namespace": "alm", "key": "REQ-1", "change": "C"})),
                Map::new(),
                HashMap::new(),
            )
            .await
            .unwrap_err();
        assert!(
            err.to_string().contains("carries no principal"),
            "{id}: {err}"
        );
    }
}

#[tokio::test]
async fn unknown_operations_are_refused() {
    let g = Arc::new(sample_graph());
    let ctx = as_user("alice");
    let change = crate::Change::new(ports(g.clone()));
    let err = change
        .call(&ctx, "nope", &args(json!({"change": "C"})))
        .await
        .unwrap_err();
    assert!(err.is_unknown_operation());
    assert_eq!(err.to_string(), "nope: unknown operation");
    let graph = crate::Graph::new(ports(g));
    let err = graph
        .call(&ctx, "nope", &args(json!({"namespace": "alm"})))
        .await
        .unwrap_err();
    assert!(err.is_unknown_operation());
}

#[tokio::test]
async fn graph_reads_what_the_caller_may_read() {
    let g = Arc::new(sample_graph());
    let mut p = ports(g.clone());
    p.authz = Some(Arc::new(Deny(vec!["alm@Secret"])));
    let graph = crate::Graph::new(p);
    let ctx = as_user("alice");

    let out = graph
        .call(
            &ctx,
            "read",
            &args(json!({"namespace": "alm", "key": "REQ-1"})),
        )
        .await
        .unwrap();
    assert_eq!(out["baseline"], "BL-alm");
    assert_eq!(out["node"]["properties"]["title"], "Login");
    // the link to the secret is shown by id only, the caller may not read its type
    assert_eq!(
        Value::Array(out["out"].as_array().unwrap().clone()),
        json!([{"type": "alm@depends_on", "key": "REQ-2", "nodeType": "alm@Requirement"}, {"type": "alm@uses", "id": "N-SEC-1"}])
    );
    let err = graph
        .call(
            &ctx,
            "read",
            &args(json!({"namespace": "alm", "key": "SEC-1"})),
        )
        .await
        .unwrap_err();
    assert_eq!(err.to_string(), "no node SEC-1 in alm (baseline BL-alm)");

    // glob: sorted by key, another namespace left out, truncated by the limit
    let out = graph
        .call(
            &ctx,
            "glob",
            &args(json!({"namespace": "alm", "pattern": "REQ-*", "limit": 1})),
        )
        .await
        .unwrap();
    assert_eq!(
        out["nodes"],
        json!([{"key": "REQ-1", "type": "alm@Requirement", "version": 1, "title": "Login"}])
    );
    assert_eq!(out["truncated"], true);
    let err = graph
        .call(
            &ctx,
            "glob",
            &args(json!({"namespace": "alm", "pattern": "["})),
        )
        .await
        .unwrap_err();
    assert_eq!(err.to_string(), r#"pattern "[": syntax error in pattern"#);

    // grep: the properties matching, case folded on demand
    let out = graph
        .call(
            &ctx,
            "grep",
            &args(json!({"namespace": "alm", "pattern": "SIGN", "ignoreCase": true})),
        )
        .await
        .unwrap();
    assert_eq!(out["nodes"][0]["matches"], json!({"text": "users sign in"}));
    assert_eq!(out["truncated"], false);
    let out = graph
        .call(
            &ctx,
            "grep",
            &args(json!({"namespace": "alm", "pattern": "SIGN"})),
        )
        .await
        .unwrap();
    assert_eq!(out["nodes"], json!([]));

    // links: one direction, one type
    let out = graph
        .call(
            &ctx,
            "links",
            &args(json!({"namespace": "alm", "key": "REQ-2", "direction": "in"})),
        )
        .await
        .unwrap();
    assert_eq!(
        out["in"],
        json!([{"type": "alm@depends_on", "key": "REQ-1", "nodeType": "alm@Requirement"}])
    );
    assert_eq!(out["out"], json!([]));

    // a baseline named is read as is, the head is not asked
    let err = graph
        .call(
            &ctx,
            "read",
            &args(json!({"namespace": "missing", "key": "X"})),
        )
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        "head of main of missing: no such namespace"
    );
    assert!(matches!(&err, Error::Graph(e) if e.is_not_found()));
    let out = graph
        .call(
            &ctx,
            "read",
            &args(json!({"namespace": "missing", "baseline": "BL-x", "key": "X"})),
        )
        .await;
    assert_eq!(
        out.unwrap_err().to_string(),
        "no node X in missing (baseline BL-x)"
    );
}

#[tokio::test]
async fn graph_lists_the_latest_baselines_first() {
    let at = |s| Utc.timestamp_opt(s, 0).unwrap();
    let b = |id: &str, s| Baseline {
        id: BaselineID::from(id),
        name: id.into(),
        created_at: at(s),
        ..Default::default()
    };
    let g = Arc::new(FakeGraph {
        baselines: vec![b("B1", 1), b("B3", 3), b("B2", 2)],
        ..Default::default()
    });
    let graph = crate::Graph::new(ports(g));
    let out = graph
        .call(
            &as_user("alice"),
            "baselines",
            &args(json!({"namespace": "alm", "limit": 2})),
        )
        .await
        .unwrap();
    assert_eq!(out["head"], "BL-alm");
    assert_eq!(
        out["baselines"],
        json!([{"id": "B3", "name": "B3", "createdAt": "1970-01-01T00:00:03Z"}, {"id": "B2", "name": "B2", "createdAt": "1970-01-01T00:00:02Z"}])
    );
}

#[tokio::test]
async fn change_needs_a_change_outside_a_process() {
    let change = crate::Change::new(ports(Arc::new(FakeGraph::default())));
    let err = change
        .call(&as_user("alice"), "read", &Map::new())
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        r#"argument "change" is required outside a process"#
    );
    // the change of the calling process
    let ctx = goap_mcp::with_call(
        &as_user("alice"),
        goap_mcp::CallContext {
            change: "CH-P".into(),
            ..Default::default()
        },
    );
    let out = change.call(&ctx, "read", &Map::new()).await.unwrap();
    assert_eq!(out["nodes"], json!([]));
}

#[tokio::test]
async fn change_gates_the_access_nodes() {
    let g = Arc::new(FakeGraph {
        admin_only: HashSet::from(["organisation@Policy".to_string()]),
        ..Default::default()
    });
    let ctx = as_user("alice");
    let write = |typ: &str| args(json!({"change": "C", "key": "K", "type": typ, "rationale": "r"}));

    // no authorizer: an access node is never written
    let change = crate::Change::new(ports(g.clone()));
    let err = change
        .call(&ctx, "write", &write("organisation@Policy"))
        .await
        .unwrap_err();
    assert!(err.is_forbidden());
    assert_eq!(
        err.to_string(),
        "organisation@Policy nodes cannot be changed without an authorizer: permission denied"
    );
    // a refusing floor
    let mut p = ports(g.clone());
    p.floor = Some(Arc::new(Deny(vec![goap_access::RESOURCE_POLICY])));
    let change = crate::Change::new(p);
    let err = change
        .call(&ctx, "write", &write("organisation@Policy"))
        .await
        .unwrap_err();
    assert!(err.is_forbidden(), "{err}");
    assert!(g.created.lock().is_empty());

    // any other type goes through
    let out = change
        .call(&ctx, "write", &write("alm@Requirement"))
        .await
        .unwrap();
    assert_eq!(out["node"]["key"], "K");
    assert_eq!(out["node"]["post"], Value::Null);
    let created = g.created.lock();
    assert_eq!(created[0].produced_by, mcpbuiltin::CHANGE);
    assert_eq!(created[0].rationale, "r");
}

#[tokio::test]
async fn change_write_refuses_a_node_already_there() {
    let mut g = sample_graph();
    g.board.change = domain::Change {
        id: ChangeID::from("C"),
        namespace: "alm".into(),
        nodes: vec![ChangeImpact {
            id: ChangeImpactID::from("CI-9"),
            key: "NEW-1".into(),
            ..Default::default()
        }],
        ..Default::default()
    };
    let change = crate::Change::new(ports(Arc::new(g)));
    let ctx = as_user("alice");
    let w = |key: &str| args(json!({"change": "C", "key": key, "type": "alm@Requirement"}));
    assert_eq!(
        change
            .call(&ctx, "write", &w("NEW-1"))
            .await
            .unwrap_err()
            .to_string(),
        "NEW-1 is already in the change: edit it"
    );
    assert_eq!(
        change
            .call(&ctx, "write", &w("REQ-1"))
            .await
            .unwrap_err()
            .to_string(),
        "REQ-1 exists in the baseline of the change: edit it"
    );
    assert_eq!(
        change
            .call(&ctx, "write", &args(json!({"change": "C", "key": "K"})))
            .await
            .unwrap_err()
            .to_string(),
        r#"arguments "key" and "type" are required"#
    );
}

#[tokio::test]
async fn change_removes_one_or_several_nodes() {
    let mut g = FakeGraph::default();
    let imp = |id: &str, key: &str| ChangeImpact {
        id: ChangeImpactID::from(id),
        key: key.into(),
        ..Default::default()
    };
    g.board.change.nodes = vec![imp("CI-1", "A"), imp("CI-2", "B")];
    let g = Arc::new(g);
    let change = crate::Change::new(ports(g.clone()));
    let ctx = as_user("alice");
    let out = change
        .call(&ctx, "remove", &args(json!({"change": "C", "key": "A"})))
        .await
        .unwrap();
    assert_eq!(out["removed"], "A");
    let out = change
        .call(
            &ctx,
            "remove",
            &args(json!({"change": "C", "key": "A", "keys": ["B", "A"]})),
        )
        .await
        .unwrap();
    assert_eq!(out["removed"], json!(["A", "B"]));
    let w = g.withdrawn.lock().clone();
    assert_eq!(w.len(), 2);
    assert_eq!(
        w[1],
        [ChangeImpactID::from("CI-1"), ChangeImpactID::from("CI-2")]
    );
    let err = change
        .call(&ctx, "remove", &args(json!({"change": "C", "keys": ["Z"]})))
        .await
        .unwrap_err();
    assert_eq!(err.to_string(), "Z is not in the change");
    let err = change
        .call(&ctx, "remove", &args(json!({"change": "C"})))
        .await
        .unwrap_err();
    assert_eq!(err.to_string(), r#"argument "key" or "keys" is required"#);
}

#[tokio::test]
async fn change_numbers_risks_and_actions() {
    let mut g = FakeGraph::default();
    let risk = |key: &str, title: &str| ChangeItem {
        r#type: goap_risk::TYPE_RISK,
        data: args(json!({"key": key, "title": title})),
        ..Default::default()
    };
    g.board.change.items = vec![risk("RSK-2", "old"), risk("RSK-7", "seven")];
    let g = Arc::new(g);
    let change = crate::Change::new(ports(g.clone()));
    let ctx = as_user("alice");
    let out = change
        .call(
            &ctx,
            "risk",
            &args(json!({"change": "C", "title": "new", "probability": 3})),
        )
        .await
        .unwrap();
    assert_eq!(
        out["risk"]["data"],
        json!({"key": "RSK-8", "title": "new", "probability": 3})
    );
    // a new version keeps the title it does not restate
    let out = change
        .call(
            &ctx,
            "risk",
            &args(json!({"change": "C", "key": "RSK-7", "status": "closed"})),
        )
        .await
        .unwrap();
    assert_eq!(
        out["risk"]["data"],
        json!({"key": "RSK-7", "title": "seven", "status": "closed"})
    );
    let err = change
        .call(&ctx, "action", &args(json!({"change": "C"})))
        .await
        .unwrap_err();
    assert_eq!(err.to_string(), "a new action needs a title");
    let out = change
        .call(
            &ctx,
            "action",
            &args(json!({"change": "C", "title": "do", "for": "RSK-7"})),
        )
        .await
        .unwrap();
    assert_eq!(out["action"]["data"]["key"], "ACT-1");
    assert_eq!(out["action"]["target"], "action");
}

#[tokio::test]
async fn change_hands_the_decision_policy_over() {
    let g = Arc::new(FakeGraph::default());
    let change = crate::Change::new(ports(g.clone()));
    let _ = change
        .call(
            &as_user("alice"),
            "decision",
            &args(json!({"change": "C", "question": "Q?", "options": ["a", 2], "decider": "", "threshold": 0.8, "maxRounds": 0})),
        )
        .await;
    assert_eq!(
        g.calls().last().unwrap(),
        r#"open_decision Q? Some(["a", "2"]) {"threshold":0.8}"#
    );
}

#[tokio::test]
async fn change_creates_in_the_callers_project() {
    let g = Arc::new(FakeGraph::default());
    let change = crate::Change::new(ports(g));
    let ctx = as_user("alice");
    let out = change
        .call(
            &ctx,
            "create",
            &args(json!({"title": "T", "intent": "I", "namespace": "alm"})),
        )
        .await
        .unwrap();
    assert_eq!(out["change"]["project"], "PROJ-A");
    assert_eq!(out["change"]["unit"], "ORG-A");
    assert_eq!(out["change"]["baselineId"], "BL-alm");
    assert_eq!(out["change"]["guardians"], Value::Null);
    let err = change
        .call(&ctx, "create", &args(json!({"title": "T"})))
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        r#"arguments "title", "intent" and "namespace" are required"#
    );
    let out = change
        .call(&ctx, "list", &args(json!({"limit": 2})))
        .await
        .unwrap();
    let ids: Vec<&str> = out["changes"]
        .as_array()
        .unwrap()
        .iter()
        .map(|c| c["id"].as_str().unwrap())
        .collect();
    assert_eq!(ids, ["CH-3", "CH-2"]);
    assert_eq!(out["truncated"], true);
}

#[tokio::test]
async fn merge_gates_every_parent_the_graph_finds() {
    let mut g = sample_graph();
    g.admin_only.insert("other@Thing".into());
    let g = Arc::new(g);
    let ctx = as_user("alice");
    let merge = args(
        json!({"change": "C", "sources": ["REQ-1"], "key": "M", "type": "alm@Requirement", "rationale": "r"}),
    );
    // a caller who may not write the access nodes: refused on the admin-only parent
    let mut p = ports(g.clone());
    p.authz = Some(Arc::new(Deny(vec![goap_access::RESOURCE_POLICY])));
    let err = crate::Change::new(p)
        .call(&ctx, "merge", &merge)
        .await
        .unwrap_err();
    assert!(err.is_forbidden(), "{err}");
    // an administrator: nothing refused
    let mut p = ports(g);
    p.authz = Some(Arc::new(Deny(vec![])));
    let out = crate::Change::new(p)
        .call(&ctx, "merge", &merge)
        .await
        .unwrap();
    assert_eq!(out["successors"], Value::Null);
    assert_eq!(out["suspect"], Value::Null);
}

#[tokio::test]
async fn scheduler_starts_for_the_caller() {
    let eng = Arc::new(FakeEngine::default());
    let engine: Arc<crate::EngineAPI> = eng.clone();
    let s = crate::Scheduler::new(Ports {
        engine: Some(engine),
        graph: Some(Arc::new(FakeGraph::default())),
        ..Default::default()
    });
    let ctx = as_user("alice");
    let out = s
        .call(
            &ctx,
            "start",
            &args(json!({"intent": "do it", "namespace": "alm", "goal": "g"})),
        )
        .await
        .unwrap();
    // the world and the steps are left out of the summary
    assert_eq!(out["process"], json!({"id": "P-1", "goal": "g"}));
    let started = eng.started.lock().clone();
    let r = &started[0];
    assert_eq!(r.msg.title, "do it");
    assert_eq!(r.msg.baseline_id, "BL-alm");
    assert_eq!(r.msg.owner_org, "ORG-A");
    assert_eq!(
        r.headers.get(goap_identity::HEADER_SUBJECT).unwrap(),
        "alice"
    );

    let err = s
        .call(&ctx, "start", &args(json!({"intent": "x"})))
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        "name the namespace (or the methodology, or the change) the process works on"
    );
    let err = s.call(&ctx, "start", &Map::new()).await.unwrap_err();
    assert_eq!(err.to_string(), r#"argument "intent" is required"#);

    let out = s
        .call(&ctx, "list", &args(json!({"limit": 2})))
        .await
        .unwrap();
    assert_eq!(out["truncated"], true);
    let ids: Vec<&str> = out["processes"]
        .as_array()
        .unwrap()
        .iter()
        .map(|p| p["id"].as_str().unwrap())
        .collect();
    assert_eq!(ids, ["new", "mid"]);
    assert!(out["processes"][0].get("steps").is_none());

    let err = s
        .call(&ctx, "get", &args(json!({"id": "P"})))
        .await
        .unwrap_err();
    assert_eq!(err.to_string(), "the engine returned no process");
    let err = s
        .call(&ctx, "fire", &args(json!({"methodology": "m"})))
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        r#"arguments "methodology", "agent" and "trigger" are required"#
    );
    let err = s.call(&ctx, "attach", &Map::new()).await.unwrap_err();
    assert_eq!(
        err.to_string(),
        r#"argument "process" is required outside a process"#
    );
    let err = s
        .call(&ctx, "starting_points", &Map::new())
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        r#"argument "changeId" is required outside a change"#
    );
    assert!(
        s.call(&ctx, "nope", &Map::new())
            .await
            .unwrap_err()
            .is_unknown_operation()
    );
}

#[tokio::test]
async fn admin_lists_the_users_of_a_unit() {
    let u = |subject: &str| {
        node(
            "organisation",
            &format!("USR:{subject}"),
            goap_access::NODE_TYPE_USER,
            json!({"subject": subject}),
        )
    };
    let unit = node(
        "organisation",
        "ORG-A",
        "organisation@OrgUnit",
        json!({"name": "A"}),
    );
    let (alice, bob) = (u("alice"), u("bob"));
    let links = vec![link(&alice, goap_access::LINK_MEMBER_OF, &unit)];
    let g = Arc::new(FakeGraph {
        nodes: vec![bob, alice, unit],
        links,
        ..Default::default()
    });
    let p = Ports {
        graph: Some(g.clone()),
        ..Default::default()
    };
    let admin = crate::Admin::new(p.clone());
    let ctx = as_user("alice");
    let out = admin.call(&ctx, "users", &Map::new()).await.unwrap();
    assert_eq!(
        out["users"],
        json!([{"subject": "alice", "displayName": "", "email": "", "unit": "ORG-A"}, {"subject": "bob", "displayName": "", "email": "", "unit": ""}])
    );
    let out = admin
        .call(&ctx, "users", &args(json!({"unit": "ORG-A"})))
        .await
        .unwrap();
    assert_eq!(out["users"].as_array().unwrap().len(), 1);
    // the read is authorized per operation
    let mut denied = p;
    denied.authz = Some(Arc::new(Deny(vec![goap_access::RESOURCE_POLICY])));
    let err = crate::Admin::new(denied)
        .call(&ctx, "users", &Map::new())
        .await
        .unwrap_err();
    assert!(err.is_forbidden());
    assert!(
        admin
            .call(&ctx, "nope", &Map::new())
            .await
            .unwrap_err()
            .is_unknown_operation()
    );
}

#[test]
fn review_ids_resolve_by_id_or_key() {
    let mut bb = Blackboard::default();
    bb.change.nodes = vec![
        ChangeImpact {
            id: ChangeImpactID::from("CI-1"),
            key: "A".into(),
            superseded: true,
            ..Default::default()
        },
        ChangeImpact {
            id: ChangeImpactID::from("CI-2"),
            key: "A".into(),
            ..Default::default()
        },
    ];
    assert_eq!(crate::change_review::impact_id(&bb, "A").unwrap(), "CI-2");
    assert_eq!(
        crate::change_review::impact_id(&bb, "CI-2").unwrap(),
        "CI-2"
    );
    assert_eq!(
        crate::change_review::impact_id(&bb, "CI-1")
            .unwrap_err()
            .to_string(),
        "CI-1 is not in the change"
    );
    let ids = crate::change_review::impact_ids(&bb, Some(&json!(["A", "CI-2"]))).unwrap();
    assert_eq!(ids, ["CI-2", "CI-2"]);
    assert!(
        crate::change_review::impact_ids(&bb, None)
            .unwrap()
            .is_empty()
    );
    let _ = NodeRef::default();
}
