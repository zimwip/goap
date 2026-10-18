//! Go: sdlc_workspace_test.go.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

use async_trait::async_trait;
use goap_connector_workspace as workspace;
use goap_connectorkit::Connector as _;
use goap_ctx::Ctx;
use goap_domain as domain;
use goap_engine::{AuthzScope, LLMExecutor, STATUS_WAITING, StartRequest, ToolPort};
use goap_llm as llm;
use goap_mcp as mcp;
use goap_mcpbuiltin as mcpbuiltin;
use goap_methodology as methodology;
use parking_lot::Mutex;
use serde_json::{Map, Value, json};

use crate::common::*;
use crate::sdlc::{SdlcSetup, dev_ctx, sdlc_model_of, sdlc_setup, validation};

/// WorkspaceHub binds the workspace MCP (and goap-graph, which the fake model never calls) for every unit and runs
/// its tools on the connector of the local machine, with a temporary root.
struct WorkspaceHub {
    conn: workspace::Connector,
    root: String,
    calls: Mutex<Vec<String>>,
}

#[async_trait]
impl ToolPort for WorkspaceHub {
    async fn call_tool(
        &self,
        ctx: &Ctx,
        _org: &str,
        name: &str,
        args: Map<String, Value>,
    ) -> goap_engine::Result<Value> {
        let (m, tool) = name.split_once('/').unwrap_or((name, ""));
        self.calls.lock().push(name.to_string());
        if m != mcpbuiltin::WORKSPACE {
            return Err(goap_engine::Error::msg(format!("unexpected tool {name}")));
        }
        if mcp::call_from(ctx).change.is_empty() {
            return Err(goap_engine::Error::msg(format!(
                "{name} runs for no change"
            )));
        }
        let config = json!({"root": self.root, "gitName": "goap", "gitEmail": "goap@example.org"});
        let Value::Object(config) = config else {
            unreachable!()
        };
        self.conn
            .invoke(ctx, tool, args, config, HashMap::new())
            .await
            .map(Value::Object)
            .map_err(|e| goap_engine::Error::msg(e.to_string()))
    }

    async fn tools(
        &self,
        _: &Ctx,
        _org: &str,
    ) -> goap_engine::Result<(Vec<mcp::ToolInfo>, Vec<String>)> {
        let def = mcpbuiltin::workspace_def();
        let out = def
            .tools
            .iter()
            .map(|t| mcp::ToolInfo {
                name: mcp::tool_name(&def.name, &t.name),
                description: t.description.clone(),
                input_schema: t.input_schema.clone(),
                read_only: t.read_only,
                ..Default::default()
            })
            .collect();
        Ok((
            out,
            vec![mcpbuiltin::WORKSPACE.to_string(), "goap-graph".to_string()],
        ))
    }
}

fn run_git(dir: &Path, args: &[&str]) -> String {
    let out = Command::new("git")
        .args(args)
        .current_dir(dir)
        .env("GIT_AUTHOR_NAME", "t")
        .env("GIT_AUTHOR_EMAIL", "t@example.org")
        .env("GIT_COMMITTER_NAME", "t")
        .env("GIT_COMMITTER_EMAIL", "t@example.org")
        .output()
        .expect("git");
    assert!(
        out.status.success(),
        "git {args:?}: {}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

/// TempDir is a temporary directory removed when dropped.
struct TempDir(PathBuf);

impl TempDir {
    fn new() -> TempDir {
        static N: AtomicUsize = AtomicUsize::new(0);
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map_or(0, |d| d.as_nanos());
        let p = std::env::temp_dir().join(format!(
            "goap-e2e-ws-{}-{nanos}-{}",
            std::process::id(),
            N.fetch_add(1, Ordering::SeqCst)
        ));
        std::fs::create_dir_all(&p).unwrap();
        TempDir(p)
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

/// Runs the delivery on a component the design gives a repository (ADR 0118): the workspace_build method opens the
/// workspace of the change with its sources, the developer agent implements the component there with the workspace
/// tools, its build is checked (a failure sends it back to the agent), and the branch of the change is pushed once
/// it builds; the other components are recorded by their technology.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn sdlc_builds_from_the_sources() {
    if Command::new("git").arg("--version").output().is_err() {
        eprintln!("git is not installed");
        return;
    }
    let tmp = TempDir::new();
    let src = tmp.0.join("src");
    std::fs::create_dir_all(&src).unwrap();
    run_git(&src, &["init", "--quiet", "--initial-branch=main"]);
    std::fs::write(src.join("README"), "installments engine\n").unwrap();
    run_git(&src, &["add", "README"]);
    run_git(&src, &["commit", "--quiet", "-m", "start"]);
    let origin = tmp.0.join("origin.git");
    run_git(
        &tmp.0,
        &[
            "clone",
            "--quiet",
            "--bare",
            src.to_str().unwrap(),
            origin.to_str().unwrap(),
        ],
    );

    let SdlcSetup {
        mut e,
        g,
        base: baseline,
        errs,
    } = Box::pin(sdlc_setup()).await;
    // the design creates CMP-10 with its sources; the agent implements it in two rounds: the first does not build
    let base = sdlc_model_of(&errs, Arc::default());
    let attempts = Arc::new(AtomicUsize::new(0));
    let origin_url = format!("file://{}", origin.to_string_lossy().replace('\\', "/"));
    let model = {
        let (errs, attempts) = (errs.clone(), attempts.clone());
        llm::ClientFunc::shared(move |ctx: Ctx, req: llm::Request| {
            let (errs, attempts, base, origin_url) = (
                errs.clone(),
                attempts.clone(),
                base.clone(),
                origin_url.clone(),
            );
            async move {
                let p = req.messages[0].content.clone();
                if p.contains("Design the evolution") {
                    let text = json!({"items": [
                        {"type": "changeImpact", "changeImpact": {"op": "declare", "ref": "#c1", "intent": "created",
                            "type": "Component", "key": "CMP-10", "rationale": "a dedicated engine"}},
                        {"type": "changeImpact", "changeImpact": {"op": "write", "node": "#c1",
                            "props": {"title": "installments-engine", "technology": "java", "version": "0.0.0",
                                "repository": origin_url, "buildCommand": "test -f feature.txt"},
                            "links": [{"type": "implements", "to": "FCT-1"}]}},
                        {"type": "artifact", "target": "design", "data": {"summary": "dedicated installment scheduling engine",
                            "decisions": ["new Java component"]}}]})
                    .to_string();
                    return Ok(llm::Response {
                        text,
                        ..Default::default()
                    });
                }
                if p.contains("You implement ONE component") {
                    let last = req.messages.last().unwrap();
                    if !last.tool_results.is_empty() {
                        return Ok(llm::Response {
                            text: r#"{"items":[{"type":"artifact","target":"code_change","data":{"component":"CMP-10","summary":"engine","files":["feature.txt"],"tests":"test -f"}}]}"#.into(),
                            ..Default::default()
                        });
                    }
                    let n = attempts.fetch_add(1, Ordering::SeqCst) + 1;
                    let mut file = "draft.txt"; // the first attempt misses what the build checks
                    if n > 1 {
                        if !p.contains("FAILED") {
                            errs.push(format!(
                                "the second attempt is not told the build failed:\n{p}"
                            ));
                        }
                        file = "feature.txt";
                    }
                    let args = |v: Value| match v {
                        Value::Object(m) => m,
                        _ => Map::new(),
                    };
                    return Ok(llm::Response {
                        stop_reason: llm::STOP_TOOL_USE.into(),
                        tool_calls: vec![
                            llm::ToolCall {
                                id: "w".into(),
                                name: "workspace/write".into(),
                                arguments: args(
                                    json!({"path": format!("CMP-10/{file}"), "content": "installments\n"}),
                                ),
                            },
                            llm::ToolCall {
                                id: "c".into(),
                                name: "workspace/bash".into(),
                                arguments: args(
                                    json!({"command": "git add -A && git commit -qm 'installments'", "cwd": "CMP-10"}),
                                ),
                            },
                        ],
                        ..Default::default()
                    });
                }
                base.complete(&ctx, req).await
            }
        })
    };
    let hub = Arc::new(WorkspaceHub {
        conn: workspace::new(vec![]),
        root: tmp.0.join("workspaces").to_string_lossy().into_owned(),
        calls: Mutex::default(),
    });
    e.executors.insert(
        methodology::KIND_LLM.into(),
        Arc::new(LLMExecutor {
            client: Some(model),
        }),
    );
    e.scope = Some(Arc::new(AuthzScope {
        authz: Some(must_casbin()),
        hub: Some(hub.clone()),
    }));
    let e = Arc::new(e);

    let ctx = dev_ctx();
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "sdlc".into(),
                agent: "delivery".into(),
                goal: "deliver".into(),
                baseline_id: baseline,
                title: "Payment in 3 installments".into(),
                intent: "Allow payment in 3 installments with no fees".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = Box::pin(e.run(&crate::common::ctx(), &p.id)).await.unwrap();
    assert!(
        p.pending
            .as_ref()
            .is_some_and(|t| t.action == "validate_impact_analysis"),
        "impact analysis: {} {} {:?}",
        p.status,
        p.error,
        p.pending
    );
    e.submit(&ctx, &p.id, vec![validation()]).await.unwrap();
    let p = Box::pin(e.run(&crate::common::ctx(), &p.id)).await.unwrap();
    let mut actions: Vec<String> = vec![];
    for s in &p.steps {
        if actions.last() != Some(&s.action) {
            actions.push(s.action.clone());
        }
    }
    assert!(
        p.status == STATUS_WAITING && p.pending.as_ref().is_some_and(|t| t.action == "review"),
        "must wait for the review: {} {} {actions:?} {:?}",
        p.status,
        p.error,
        p.pending
    );
    // the build goes back to the agent once, then publishes; the recorded build follows for the others (other
    // steps may come in between: only the order of these matters)
    let steps = [
        "prepare_workspace",
        "implement_component",
        "build_component",
        "publish_build",
        "build",
    ];
    let build: Vec<&str> = actions
        .iter()
        .map(String::as_str)
        .filter(|a| steps.contains(a))
        .collect();
    assert_eq!(
        build,
        [
            "prepare_workspace",
            "implement_component",
            "build_component",
            "implement_component",
            "build_component",
            "publish_build",
            "build"
        ],
        "build steps (steps {actions:?})"
    );
    assert_eq!(attempts.load(Ordering::SeqCst), 2, "attempts");

    let c = g.change(&ctx, &p.change_id).await.unwrap();
    let (mut reports, mut builds, mut artifacts) = (0, 0, 0);
    for it in &c.items {
        match it.target.as_str() {
            "build_report" => {
                reports += 1;
                let ok = it.data.get("ok") == Some(&Value::Bool(true));
                assert_eq!(ok, reports == 2, "build report {reports}: {:?}", it.data);
            }
            "build" => builds += 1,
            _ => {}
        }
    }
    let mut art = String::new();
    for n in &c.nodes {
        if n.r#type == "alm@BuildArtifact" && n.intent == domain::INTENT_CREATED {
            artifacts += 1;
            if n.key.starts_with("ART-CMP-10-") {
                art = n.key.clone();
            }
        }
    }
    // CMP-10 from its sources; CMP-1 (java), CMP-4 (c), CMP-2 (go, generic) by their technology
    assert!(
        reports == 2 && builds == 4 && artifacts == 4 && !art.is_empty(),
        "reports {reports}, builds {builds}, artifacts {artifacts} ({art:?})"
    );
    // the branch of the change reached the remote with the work of the agent, and nothing else did
    let branch = format!("goap/{}", p.change_id);
    let files = run_git(&origin, &["ls-tree", "--name-only", &branch]);
    assert_eq!(files, "README\ndraft.txt\nfeature.txt", "files on {branch}");
    let heads = run_git(
        &origin,
        &["for-each-ref", "--format=%(refname:short)", "refs/heads"],
    );
    assert!(
        heads == format!("{branch}\nmain") || heads == format!("main\n{branch}"),
        "branches of the remote: {heads:?}"
    );
    let calls = hub.calls.lock().clone();
    assert!(
        calls.iter().any(|c| c == "workspace/push"),
        "calls {calls:?}"
    );
}
