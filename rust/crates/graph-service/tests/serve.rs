use std::sync::Arc;

use graph_service::{State, app, domains_app};
use graph_store::{Store, StoreConfig};

/// A graph on a database in memory, its domains read from a registry in this process (ADR 0167): the graph's state,
/// the registry's.
async fn states() -> (Arc<State>, Arc<State>) {
    let store = Store::open(&StoreConfig::Memory).await.unwrap();
    let (graph, registry) = State::with_local_domains(store, &graph_catalog::catalog::builtin_dir())
        .await
        .unwrap();
    (Arc::new(graph), registry)
}

async fn listen(router: axum::Router) -> String {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(axum::serve(listener, router).into_future());
    format!("http://{addr}")
}

/// The graph service and the domain registry, each at its address.
async fn serve() -> (String, String) {
    let (graph, registry) = states().await;
    (listen(app(graph)).await, listen(domains_app(registry)).await)
}

#[tokio::test]
async fn health_and_the_guards_judged_here() {
    let (graph, base) = serve().await;
    let c = reqwest::Client::new();
    for b in [&graph, &base] {
        assert_eq!(c.get(format!("{b}/healthz")).send().await.unwrap().status(), 200);
        assert_eq!(c.get(format!("{b}/readyz")).send().await.unwrap().status(), 200);
    }
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
    let (base, _) = serve().await;
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
    let (base, _) = serve().await;
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
    let (state, _registry) = states().await;
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
    let (state, _registry) = states().await;
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

#[tokio::test]
async fn a_protected_domain_refuses_every_write_over_rpc() {
    let (_, base) = serve().await;
    let c = reqwest::Client::new();
    let post = |method: &str, body: serde_json::Value| {
        c.post(format!("{base}/goap.registry.v1.DomainService/{method}"))
            .header("content-type", "application/json")
            .header("x-goap-subject", ADMIN)
            .header("x-goap-roles", "admin")
            .body(body.to_string())
            .send()
    };
    // the built-in domains are stored, published and protected
    let res = post("GetDomain", serde_json::json!({"name": "organisation"}))
        .await
        .unwrap();
    assert_eq!(res.status(), 200);
    let body: serde_json::Value = res.json().await.unwrap();
    assert_eq!(body["domain"]["status"], "published", "{body}");
    assert_eq!(body["domain"]["protected"], true, "{body}");
    for (method, body) in [
        (
            "SaveDomain",
            serde_json::json!({"domain": {"name": "organisation", "version": "99", "nodeTypes": [{"name": "X"}]}}),
        ),
        (
            "DeleteDomain",
            serde_json::json!({"name": "organisation", "version": ""}),
        ),
        (
            "CreateDomainVersion",
            serde_json::json!({"name": "organisation", "newVersion": "99"}),
        ),
    ] {
        let res = post(method, body).await.unwrap();
        assert_eq!(res.status(), 400, "{method}");
        let body: serde_json::Value = res.json().await.unwrap();
        assert_eq!(body["code"], "failed_precondition", "{method}: {body}");
        assert!(
            body["message"].as_str().unwrap_or_default().contains("is protected"),
            "{method}: {body}"
        );
    }
}

#[tokio::test]
async fn a_change_follows_what_its_methodology_declares() {
    let (state, _registry) = states().await;
    graph_service::Graph { state: state.clone() }
        .boot(&Default::default())
        .await
        .unwrap();
    let base = listen(app(state)).await;
    let svc = Some(ADMIN);
    let header = |name: &str, version: &str, at: &str, extra: serde_json::Value| {
        let mut props = serde_json::json!({"name": name, "version": version, "status": "published", "publishedAt": at,
            "namespace": "platform"});
        props
            .as_object_mut()
            .unwrap()
            .extend(extra.as_object().unwrap().clone());
        serde_json::json!({"key": format!("MV:{name}@{version}"), "type": "methodology@MethodologyVersion", "props": props})
    };
    // a methodology in two versions, the later one with a lifecycle and a goal; another integrating it
    let edits = serde_json::json!([
        header("work", "1.0.0", "2026-01-01T00:00:00Z", serde_json::json!({})),
        header("work", "1.1.0", "2026-02-01T00:00:00Z", serde_json::json!({"lifecycle": "config"})),
        {"key": "MV:work@1.1.0/goal/second", "type": "methodology@Goal", "props": {"name": "second", "position": 1}},
        {"key": "MV:work@1.1.0/goal/first", "type": "methodology@Goal", "props": {"name": "first", "position": 0}},
        header("release", "2.0.0", "2026-01-01T00:00:00Z",
            serde_json::json!({"integrates": [{"methodologies": ["work"], "exclusive": true}]})),
    ]);
    let (status, body) = call(
        &base,
        "CommitEdits",
        svc,
        serde_json::json!({"namespace": "methodology", "title": "declare", "projectId": "PROJ-ROOT", "edits": edits}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    let (status, body) = call(
        &base,
        "CommitEdits",
        svc,
        serde_json::json!({"namespace": "organisation", "title": "project", "projectId": "PROJ-ROOT", "edits": [
            {"key": "PROJ-W", "type": "organisation@ProjectUnit", "props": {"name": "W", "methodologies": ["work", "release"]},
             "links": [{"type": "organisation@project_part_of", "toKey": "PROJ-ROOT"}]}]}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    let create = async |methodology: &str, version: &str| {
        call(
            &base,
            "CreateChange",
            svc,
            serde_json::json!({"title": "c", "namespace": "platform", "ownBranch": true, "projectId": "PROJ-W",
                "methodology": methodology, "methodologyVersion": version}),
        )
        .await
    };
    let (status, body) = create("work", "").await;
    assert_eq!(status, 200, "{body}");
    let c = &body["change"];
    assert_eq!(c["methodologyVersion"], "1.1.0", "{body}");
    assert_eq!(c["goal"], "first", "{body}");
    assert_eq!(
        (&c["lifecycle"], &c["state"]),
        (&"config".into(), &"active".into()),
        "{body}"
    );
    assert_eq!(
        c["guardians"],
        serde_json::json!([{"methodology": "release", "version": "2.0.0"}, {"methodology": "work", "version": "1.1.0"}]),
        "{body}"
    );
    let id = c["id"].as_str().unwrap().to_string();
    // the earlier version names no lifecycle: an upgrade to it would leave the change where no lifecycle knows it
    let (_, body) = call(
        &base,
        "UpgradeChange",
        svc,
        serde_json::json!({"changeId": id, "version": "1.0.0"}),
    )
    .await;
    assert_eq!(body["code"], "failed_precondition", "{body}");
    let (status, body) = create("work", "9.9.9").await;
    assert_eq!(status, 400, "{body}");
    assert!(
        body["message"]
            .as_str()
            .unwrap()
            .contains("methodology work has no version 9.9.9"),
        "{body}"
    );
    // the earlier version: no lifecycle, its goal none
    let (status, body) = create("work", "1.0.0").await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(body["change"]["lifecycle"], serde_json::Value::Null, "{body}");
    let old = body["change"]["id"].as_str().unwrap().to_string();
    let (_, body) = call(&base, "UpgradeChange", svc, serde_json::json!({"changeId": old})).await;
    assert_eq!(body["code"], "failed_precondition", "{body}");
    // a change attaches under a change whose methodology integrates its own, never the other way
    let (status, body) = create("release", "").await;
    assert_eq!(status, 200, "{body}");
    let release = body["change"]["id"].as_str().unwrap().to_string();
    let (_, body) = call(
        &base,
        "AttachChange",
        svc,
        serde_json::json!({"changeId": release, "parentId": old}),
    )
    .await;
    assert_eq!(body["code"], "invalid_argument", "{body}");
    assert!(
        body["message"]
            .as_str()
            .unwrap()
            .contains("does not integrate the changes of"),
        "{body}"
    );
    let (status, body) = call(
        &base,
        "AttachChange",
        svc,
        serde_json::json!({"changeId": old, "parentId": release}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    // detached, its chain is computed again for its project
    let (status, body) = call(&base, "DetachChange", svc, serde_json::json!({"changeId": old})).await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(
        body["change"]["guardians"],
        serde_json::json!([{"methodology": "release", "version": "2.0.0"}, {"methodology": "work", "version": "1.0.0"}]),
        "{body}"
    );
    // a methodology no one declares: no version, no lifecycle, its own guardian
    let (status, body) = create("unknown", "").await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(
        body["change"]["guardians"],
        serde_json::json!([{"methodology": "unknown"}]),
        "{body}"
    );
}

#[tokio::test]
async fn a_change_moves_along_the_lifecycle_its_methodology_names() {
    let (state, registry) = states().await;
    graph_service::Graph { state: state.clone() }
        .boot(&Default::default())
        .await
        .unwrap();
    let base = listen(app(state)).await;
    let domains = listen(domains_app(registry)).await;
    let svc = Some(ADMIN);
    let c = reqwest::Client::new();
    // a domain whose lifecycle guards its move with a condition of the methodology, and ends in a final state
    let domain = serde_json::json!({"name": "flow", "version": "1.0.0",
        "nodeTypes": [{"name": "Note"}],
        "lifecycles": [{"name": "review", "initial": "drafting",
            "states": [{"name": "drafting"}, {"name": "reviewed"}, {"name": "done", "final": true}],
            "transitions": [
                {"name": "submit", "from": "drafting", "to": "reviewed", "guard": "world[\"titled\"]"},
                {"name": "rework", "from": "reviewed", "to": "drafting"},
                {"name": "finish", "from": "reviewed", "to": "done"}]}]});
    for (method, body) in [
        ("SaveDomain", serde_json::json!({"domain": domain})),
        ("PublishDomain", serde_json::json!({"name": "flow", "version": "1.0.0"})),
    ] {
        let res = c
            .post(format!("{domains}/goap.registry.v1.DomainService/{method}"))
            .header("content-type", "application/json")
            .header("x-goap-subject", ADMIN)
            .header("x-goap-roles", "admin")
            .body(body.to_string())
            .send()
            .await
            .unwrap();
        assert_eq!(res.status(), 200, "{method}: {:?}", res.text().await);
    }
    // the methodology the registry declares: the lifecycle of that domain and the condition its guard reads
    let edits = serde_json::json!([
        {"key": "MV:review@1.0.0", "type": "methodology@MethodologyVersion",
         "props": {"name": "review", "version": "1.0.0", "status": "published", "publishedAt": "2026-01-01T00:00:00Z",
                   "namespace": "platform", "lifecycle": "review"}},
        {"key": "MV:review@1.0.0/condition/titled", "type": "methodology@Condition",
         "props": {"name": "titled", "expr": "change.title != \"c\"", "description": "the change has a title of its own"}},
    ]);
    let (status, body) = call(
        &base,
        "CommitEdits",
        svc,
        serde_json::json!({"namespace": "methodology", "title": "declare", "projectId": "PROJ-ROOT", "edits": edits}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    let (status, body) = call(
        &base,
        "CommitEdits",
        svc,
        serde_json::json!({"namespace": "organisation", "title": "project", "projectId": "PROJ-ROOT", "edits": [
            {"key": "PROJ-R", "type": "organisation@ProjectUnit", "props": {"name": "R", "methodologies": ["review"]},
             "links": [{"type": "organisation@project_part_of", "toKey": "PROJ-ROOT"}]}]}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    let (status, body) = call(
        &base,
        "CreateChange",
        svc,
        serde_json::json!({"title": "c", "namespace": "platform", "ownBranch": true, "projectId": "PROJ-R",
            "methodology": "review"}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(
        (&body["change"]["lifecycle"], &body["change"]["state"]),
        (&"review".into(), &"drafting".into()),
        "{body}"
    );
    let id = body["change"]["id"].as_str().unwrap().to_string();
    let move_to = async |transition: &str| {
        call(
            &base,
            "TransitionChange",
            svc,
            serde_json::json!({"changeId": id, "transition": transition}),
        )
        .await
    };
    // a transition the state has not
    let (status, body) = move_to("finish").await;
    assert_eq!(status, 400, "{body}");
    assert!(
        body["message"]
            .as_str()
            .unwrap()
            .contains("has no transition \"finish\""),
        "{body}"
    );
    // its guard reads the world state of the conditions of the methodology: the title is still the one it refuses
    let (_, body) = move_to("submit").await;
    assert_eq!(body["code"], "failed_precondition", "{body}");
    assert!(
        body["message"].as_str().unwrap().contains("gate not satisfied"),
        "{body}"
    );
    let (status, body) = call(
        &base,
        "UpdateChange",
        svc,
        serde_json::json!({"id": id, "title": "a change of its own"}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    let (status, body) = move_to("submit").await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(body["change"]["state"], "reviewed", "{body}");
    // the move is journaled as a transition item of the change
    let (status, body) = call(
        &base,
        "ListChangeLog",
        svc,
        serde_json::json!({"changeId": id, "types": ["fact.transition"]}),
    )
    .await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(body["entries"][0]["subject"], "transition.submit", "{body}");
    let (status, body) = move_to("finish").await;
    assert_eq!(status, 200, "{body}");
    assert_eq!(body["change"]["state"], "done", "{body}");
}
