use std::sync::Arc;

use graph_service::{State, app};
use graph_store::{Store, StoreConfig};

async fn serve() -> String {
    let store = Store::open(&StoreConfig::Memory).await.unwrap();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(axum::serve(listener, app(Arc::new(State::new(store)))).into_future());
    format!("http://{addr}")
}

#[tokio::test]
async fn health_and_the_guards_judged_here() {
    let base = serve().await;
    let c = reqwest::Client::new();
    assert_eq!(c.get(format!("{base}/healthz")).send().await.unwrap().status(), 200);
    assert_eq!(c.get(format!("{base}/readyz")).send().await.unwrap().status(), 200);
    // a guard that does not parse is refused at its path; one that parses is accepted
    let validate = |guard: &str| {
        let d = serde_json::json!({"domain": {"name": "d", "version": "1",
            "nodeTypes": [{"name": "Item", "lifecycle": "lc"}],
            "lifecycles": [{"name": "lc", "initial": "a", "states": [{"name": "a"}],
                "transitions": [{"name": "t", "from": "a", "to": "a", "guard": guard}]}]}});
        c.post(format!("{base}/goap.registry.v1.DomainService/ValidateDomain"))
            .header("content-type", "application/json")
            .body(d.to_string())
            .send()
    };
    let res = validate("a ==").await.unwrap();
    assert_eq!(res.status(), 200);
    let body: serde_json::Value = res.json().await.unwrap();
    assert_eq!(
        body["issues"][0]["path"],
        serde_json::json!("lifecycles[0].transitions[0].guard"),
        "{body}"
    );
    let res = validate("nope == 1").await.unwrap();
    assert_eq!(res.status(), 200);
    let body: serde_json::Value = res.json().await.unwrap();
    assert_eq!(body, serde_json::json!({}), "{body}");
}

async fn call(base: &str, method: &str, subject: Option<&str>, body: serde_json::Value) -> (u16, serde_json::Value) {
    let mut req = reqwest::Client::new()
        .post(format!("{base}/goap.graph.v1.GraphService/{method}"))
        .header("content-type", "application/json")
        .body(body.to_string());
    if let Some(s) = subject {
        req = req.header("x-goap-subject", s);
        if s == ADMIN {
            req = req.header("x-goap-roles", "admin");
        }
    }
    let res = req.send().await.unwrap();
    (res.status().as_u16(), res.json().await.unwrap())
}

#[tokio::test]
async fn reads_of_a_service_and_of_a_person() {
    let base = serve().await;
    let svc = Some("system:test");
    // an anonymous call is refused, as the Go service's Identify does
    let (status, body) = call(&base, "GetNode", None, serde_json::json!({})).await;
    assert_eq!((status, body["code"].as_str()), (401, Some("unauthenticated")));
    // a read naming no view
    let (status, body) = call(&base, "GetNode", svc, serde_json::json!({"key": "R-1"})).await;
    assert_eq!(
        (status, body["code"].as_str()),
        (400, Some("invalid_argument")),
        "{body}"
    );
    assert!(
        body["message"].as_str().unwrap().starts_with("no view: name a branch"),
        "{body}"
    );
    // a node main does not hold
    let view = serde_json::json!({"namespace": "alm", "branch": "main"});
    let (status, body) = call(&base, "GetNode", svc, serde_json::json!({"key": "R-1", "view": view})).await;
    assert_eq!((status, body["code"].as_str()), (404, Some("not_found")), "{body}");
    // a person reads through its reach: a graph with no organisation yet makes no user, the read goes on
    let (status, body) = call(
        &base,
        "GetNode",
        Some("ann"),
        serde_json::json!({"key": "R-1", "view": view}),
    )
    .await;
    assert_eq!((status, body["code"].as_str()), (404, Some("not_found")), "{body}");
    // the head of main before any change: the empty state
    let (status, body) = call(
        &base,
        "GetBranch",
        svc,
        serde_json::json!({"namespace": "alm", "name": "main", "headerOnly": true}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(body["branch"]["name"], "main");
    assert!(body["head"]["id"].is_null(), "{body}");
    // no change yet
    let (status, body) = call(
        &base,
        "ListChanges",
        svc,
        serde_json::json!({"headerOnly": true, "limit": 10}),
    )
    .await;
    assert_eq!((status, &body), (200, &serde_json::json!({})));
}

async fn tags(base: &str, method: &str, subject: &str, body: serde_json::Value) -> (u16, serde_json::Value) {
    let res = reqwest::Client::new()
        .post(format!("{base}/goap.smarttag.v1.SmartTagService/{method}"))
        .header("content-type", "application/json")
        .header("x-goap-subject", subject)
        .body(body.to_string())
        .send()
        .await
        .unwrap();
    (res.status().as_u16(), res.json().await.unwrap())
}

#[tokio::test]
async fn smart_tags_are_their_owners_and_shared() {
    let base = serve().await;
    let (status, body) = tags(
        &base,
        "CreateSmartTag",
        "ann",
        serde_json::json!({"name": "  release  "}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    let id = body["smartTag"]["id"].as_str().unwrap().to_string();
    assert_eq!(
        (body["smartTag"]["name"].as_str(), body["smartTag"]["access"].as_str()),
        (Some("release"), Some("owner"))
    );
    // another user sees nothing until it is shared, then reads but does not edit
    let (status, _) = tags(&base, "GetSmartTag", "bob", serde_json::json!({"id": id})).await;
    assert_eq!(status, 404);
    let (status, body) = tags(
        &base,
        "ShareSmartTag",
        "ann",
        serde_json::json!({"id": id, "subject": "bob", "access": "read"}),
    )
    .await;
    assert_eq!(
        (status, body["smartTag"]["shares"][0]["subject"].as_str()),
        (200, Some("bob"))
    );
    let (_, body) = tags(&base, "ListSmartTags", "bob", serde_json::json!({})).await;
    assert_eq!(body["smartTags"][0]["access"], "read");
    let (status, body) = tags(
        &base,
        "UpdateSmartTag",
        "bob",
        serde_json::json!({"id": id, "name": "x"}),
    )
    .await;
    assert_eq!((status, body["code"].as_str()), (403, Some("permission_denied")));
    // a version that does not exist is refused, an empty name too
    let (status, _) = tags(
        &base,
        "UpdateSmartTag",
        "ann",
        serde_json::json!({"id": id, "add": [{"nodeId": uuid::Uuid::new_v4().to_string(), "version": 1}]}),
    )
    .await;
    assert_eq!(status, 400);
    let (status, _) = tags(&base, "CreateSmartTag", "ann", serde_json::json!({"name": " "})).await;
    assert_eq!(status, 400);
    let (status, _) = tags(&base, "DeleteSmartTag", "bob", serde_json::json!({"id": id})).await;
    assert_eq!(status, 403);
    let (status, _) = tags(&base, "DeleteSmartTag", "ann", serde_json::json!({"id": id})).await;
    assert_eq!(status, 200);
    let (_, body) = tags(&base, "ListSmartTags", "ann", serde_json::json!({})).await;
    assert!(body.get("smartTags").is_none(), "{body}");
}

/// A platform service holding the admin role: the writes of a test.
const ADMIN: &str = "system:test-admin";

#[tokio::test]
async fn a_partial_commit_writes_the_nodes_named() {
    let store = Store::open(&StoreConfig::Memory).await.unwrap();
    let state = Arc::new(State::new(store));
    graph_service::Graph { state: state.clone() }
        .boot(&Default::default())
        .await
        .unwrap();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(axum::serve(listener, app(state)).into_future());
    let base = format!("http://{addr}");
    let svc = Some(ADMIN);
    let (status, body) = call(
        &base,
        "CreateChange",
        svc,
        serde_json::json!({"title": "partial", "namespace": "platform", "ownBranch": true, "projectId": "PROJ-ROOT"}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    let change = body["change"]["id"].as_str().unwrap().to_string();
    let mut nodes = Vec::new();
    for key in ["FP:one", "FP:two"] {
        let (status, body) = call(
            &base,
            "ImpactNodeCreate",
            svc,
            serde_json::json!({"changeId": change, "key": key, "type": "platform@FormPattern", "rationale": "new"}),
        )
        .await;
        assert_eq!(status, 200, "{body}");
        let impact = body["node"]["id"].as_str().unwrap().to_string();
        nodes.push(body["node"]["post"]["id"].as_str().unwrap().to_string());
        let (status, body) = call(
            &base,
            "ImpactNodeReview",
            svc,
            serde_json::json!({"changeId": change, "changeImpactId": impact, "accept": true, "comment": "ok"}),
        )
        .await;
        assert_eq!(status, 200, "{body}");
    }
    let state = async || call(&base, "GetCommitState", svc, serde_json::json!({"changeId": change})).await;
    let (status, body) = state().await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(body["candidates"].as_array().map(Vec::len), Some(2), "{body}");
    assert_eq!(body["candidates"][0]["kind"], "created", "{body}");
    // a node with nothing to commit is refused
    let (status, body) = call(
        &base,
        "CommitChange",
        svc,
        serde_json::json!({"changeId": change, "nodes": [nodes[0], "no-such-node"]}),
    )
    .await;
    assert_eq!(
        (status, body["code"].as_str()),
        (400, Some("invalid_argument")),
        "{body}"
    );
    assert!(
        body["message"]
            .as_str()
            .unwrap()
            .contains("nothing to commit for node(s) no-such-node"),
        "{body}"
    );
    let (status, body) = call(
        &base,
        "CommitChange",
        svc,
        serde_json::json!({"changeId": change, "nodes": [nodes[0]]}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    let (_, body) = state().await;
    assert_eq!(body["candidates"].as_array().map(Vec::len), Some(1), "{body}");
    assert_eq!(body["candidates"][0]["nodeId"], nodes[1].as_str(), "{body}");
    assert_eq!(body["canCommit"], true, "{body}");
}

#[tokio::test]
async fn a_commit_rebases_its_drafts_onto_the_head() {
    let store = Store::open(&StoreConfig::Memory).await.unwrap();
    let state = Arc::new(State::new(store));
    graph_service::Graph { state: state.clone() }
        .boot(&Default::default())
        .await
        .unwrap();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(axum::serve(listener, app(state)).into_future());
    let base = format!("http://{addr}");
    let svc = Some(ADMIN);
    let ok = async |method: &str, body: serde_json::Value| {
        let (status, body) = call(&base, method, svc, body).await;
        assert_eq!(status, 200, "{method}: {body}");
        body
    };
    let open = async |title: &str| {
        ok(
            "CreateChange",
            serde_json::json!({"title": title, "namespace": "platform", "ownBranch": true, "projectId": "PROJ-ROOT"}),
        )
        .await["change"]["id"]
            .as_str()
            .unwrap()
            .to_string()
    };
    let review = async |change: &str, impact: &str| {
        ok(
            "ImpactNodeReview",
            serde_json::json!({"changeId": change, "changeImpactId": impact, "accept": true, "comment": "ok"}),
        )
        .await;
    };
    // the node, on main
    let seed = open("seed").await;
    let created = ok(
        "ImpactNodeCreate",
        serde_json::json!({"changeId": seed, "key": "FP:rebased", "type": "platform@FormPattern", "rationale": "new"}),
    )
    .await;
    let node = created["node"]["post"]["id"].as_str().unwrap().to_string();
    review(&seed, created["node"]["id"].as_str().unwrap()).await;
    ok("CommitChange", serde_json::json!({"changeId": seed})).await;
    ok("MergeChange", serde_json::json!({"changeId": seed})).await;
    // two changes from that version: A edits its label, B its description and retires it; C edits the label too
    let edit = async |title: &str, props: serde_json::Value, retire: bool| {
        let change = open(title).await;
        let impact = ok(
            "ImpactNodeCheckout",
            serde_json::json!({"changeId": change, "nodeId": node, "rationale": title}),
        )
        .await["node"]["id"]
            .as_str()
            .unwrap()
            .to_string();
        ok(
            "ImpactNodeUpdate",
            serde_json::json!({"changeId": change, "changeImpactId": impact, "props": props}),
        )
        .await;
        if retire {
            ok(
                "ImpactNodeTransition",
                serde_json::json!({"changeId": change, "changeImpactId": impact, "state": "retired"}),
            )
            .await;
        }
        review(&change, &impact).await;
        (change, impact)
    };
    let (a, _) = edit("A", serde_json::json!({"label": "from A"}), false).await;
    let (b, _) = edit("B", serde_json::json!({"description": "from B"}), true).await;
    let (c, c_impact) = edit("C", serde_json::json!({"label": "from C"}), false).await;
    ok("CommitChange", serde_json::json!({"changeId": a})).await;
    ok("MergeChange", serde_json::json!({"changeId": a})).await;
    let head = |body: &serde_json::Value| body["view"]["node"].clone();
    let at_main = async || {
        head(
            &ok(
                "GetNode",
                serde_json::json!({"ref": {"id": node}, "view": {"namespace": "platform", "branch": "main"}}),
            )
            .await,
        )
    };
    let after_a = at_main().await;
    // B's draft is rebased onto A's version at its commit: its version derives from A's alone, the merge fast-forwards
    ok("CommitChange", serde_json::json!({"changeId": b})).await;
    let merged = ok("MergeChange", serde_json::json!({"changeId": b})).await;
    let b_head = at_main().await;
    assert_eq!(b_head["props"]["label"], "from A", "{b_head}");
    assert_eq!(b_head["props"]["description"], "from B", "{b_head}");
    assert_eq!(b_head["state"], "retired", "{b_head}");
    assert_eq!(b_head["parents"], serde_json::json!([after_a["version"]]), "{b_head}");
    assert_eq!(b_head["reason"], "derive", "{b_head}");
    // the merge leaves the change open, its close applies it (ADR 0166)
    assert_eq!(merged["change"]["status"], "active", "{merged}");
    let st = ok("GetCommitState", serde_json::json!({"changeId": b})).await;
    assert!(st.get("unmerged").is_none() && st["canClose"] == true, "{st}");
    let closed = ok("CloseChange", serde_json::json!({"changeId": b})).await;
    assert_eq!(closed["change"]["status"], "applied", "{closed}");
    assert_eq!(
        at_main().await["version"],
        b_head["version"],
        "a close with nothing left to merge writes nothing"
    );
    // C changed A's label too: the commit stops on the conflict, the rebase kept
    let (status, body) = call(&base, "CommitChange", svc, serde_json::json!({"changeId": c})).await;
    assert_eq!(
        (status, body["code"].as_str()),
        (400, Some("failed_precondition")),
        "{body}"
    );
    assert!(
        body["message"]
            .as_str()
            .unwrap()
            .contains("with conflicts on FP:rebased (props.label)"),
        "{body}"
    );
    let rs = ok("GetRebaseState", serde_json::json!({"changeId": c})).await;
    assert_eq!(rs["conflicts"][0]["changeImpactId"], c_impact.as_str(), "{rs}");
    assert_eq!(at_main().await["version"], b_head["version"]);
}
