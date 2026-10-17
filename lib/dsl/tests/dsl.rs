//! The Go scripts of the Go suite (`TestGo`, `TestGoChangeImpacts`) are JavaScript
//! here: the Go language left the DSL, which `go_scripts_are_refused` checks.

use std::{sync::Arc, time::Duration};

use async_trait::async_trait;
use goap_dsl::{
    AgentResult, ChangeImpact, CompleteRequest, CompleteResult, Error, Host, Job, Link, Node, run,
};
use parking_lot::Mutex;
use serde_json::{Map, Value, json};

#[derive(Default)]
struct FakeHost {
    prompts: Mutex<Vec<String>>,
}

#[async_trait]
impl Host for FakeHost {
    async fn complete(
        &self,
        _: &goap_ctx::Ctx,
        r: CompleteRequest,
    ) -> Result<CompleteResult, Error> {
        self.prompts.lock().push(r.prompt.clone());
        Ok(CompleteResult {
            text: r#"ok {"n": 2}"#.into(),
            model: r.model,
            input_tokens: 10,
            output_tokens: 3,
            ..Default::default()
        })
    }
    async fn run_agent(
        &self,
        _: &goap_ctx::Ctx,
        name: &str,
        _: &str,
    ) -> Result<AgentResult, Error> {
        if name == "slow" {
            return Err(Error::Suspended);
        }
        Ok(AgentResult {
            status: "completed".into(),
            process_id: "child".into(),
            ..Default::default()
        })
    }
    async fn call_tool(
        &self,
        _: &goap_ctx::Ctx,
        _: &str,
        _: Map<String, Value>,
    ) -> Result<Value, Error> {
        Ok("tool".into())
    }
    async fn node(&self, _: &goap_ctx::Ctx, key: &str) -> Result<Node, Error> {
        Ok(Node {
            key: key.into(),
            r#type: "Requirement".into(),
            props: json!({"title": "t"}).as_object().cloned().unwrap(),
            ..Default::default()
        })
    }
    async fn nodes(&self, _: &goap_ctx::Ctx, _: &str) -> Result<Vec<Node>, Error> {
        Ok(vec![Node {
            key: "REQ-1".into(),
            r#type: "Requirement".into(),
            ..Default::default()
        }])
    }
    async fn links(
        &self,
        _: &goap_ctx::Ctx,
        _: &str,
        _: &str,
        _: &str,
    ) -> Result<Vec<Link>, Error> {
        Ok(vec![])
    }
}

fn node(key: &str, typ: &str, props: Value) -> Node {
    Node {
        key: key.into(),
        r#type: typ.into(),
        props: props.as_object().cloned().unwrap_or_default(),
        ..Default::default()
    }
}

fn job() -> Job {
    Job {
        action: "a".into(),
        nodes: vec![
            ChangeImpact {
                id: "n1".into(),
                key: "REQ-1".into(),
                r#type: "Requirement".into(),
                intent: "modified".into(),
                planned: true,
                pre: Some(node("REQ-1", "Requirement", json!({"title": "Pay"}))),
                ..Default::default()
            },
            ChangeImpact {
                id: "n2".into(),
                key: "TST-1".into(),
                r#type: "TestCase".into(),
                intent: "modified".into(),
                planned: true,
                pre: Some(node("TST-1", "TestCase", Value::Null)),
                ..Default::default()
            },
        ],
        ..Default::default()
    }
}

fn bg() -> goap_ctx::Ctx {
    goap_ctx::Ctx::background()
}

#[tokio::test]
async fn java_script() {
    let mut j = job();
    j.language = "javascript".into();
    j.code = r#"
function run(ctx) {
  let n = 0;
  for (const c of ctx.changeImpacts()) {
    if (c.type !== "Requirement") continue;
    const t = ctx.impactNodeCreate("TestCase", "TST-" + c.key, "verifies " + c.key);
    ctx.writeNode(t, { props: { title: "Verify " + c.pre.props.title }, links: [{ type: "verifies", to: c.key }] });
    n++;
  }
  const r = ctx.complete({ prompt: "hello", json: true });
  console.log("tokens", r.inputTokens, r.json.n);
  return n + " test(s)";
}"#
    .into();
    let h = FakeHost::default();
    let (res, err) = run(&bg(), j, &h).await;
    err.unwrap();
    assert!(
        res.nodes.len() == 2 && res.output == "1 test(s)" && h.prompts.lock().len() == 1,
        "unexpected result {res:?}"
    );
    assert!(
        res.nodes[0].op == "declare"
            && res.nodes[1].op == "write"
            && res.nodes[1].node == res.nodes[0].r#ref
            && res.nodes[1].links[0].to == "REQ-1",
        "bad operations {:?}",
        res.nodes
    );
    assert_eq!(res.nodes[1].props["title"], "Verify Pay");
    assert_eq!(res.logs.len(), 1, "logs {:?}", res.logs);
    assert_eq!(res.logs[0].message, "tokens10 2");
}

/// `TestGo`, the script in JavaScript: top-level code, a host read, a console line, an artifact.
#[tokio::test]
async fn top_level_script() {
    let mut j = job();
    j.language = "js".into();
    j.code = r##"
for (const n of ctx.changeImpacts()) {
  if (n.type !== "Requirement") continue;
  const t = ctx.impactNodeCreate("TestCase", "TST-" + n.key.toLowerCase(), "verifies");
  ctx.writeNode(t, { props: { title: "x" } });
}
const n = ctx.node("REQ-1");
console.log("node", n.type);
ctx.addArtifact("report", { markdown: "# ok" });
"##
    .into();
    let (res, err) = run(&bg(), j, &FakeHost::default()).await;
    err.unwrap();
    assert!(
        res.items.len() == 1
            && res.nodes.len() == 2
            && res.logs.len() == 1
            && res.logs[0].message == "nodeRequirement",
        "unexpected result {res:?}"
    );
    assert_eq!(res.items[0]["target"], "report");
    assert_eq!(res.items[0]["ref"], "p1");
    assert_eq!(res.items[0]["producedBy"], "a");
    assert_eq!(res.output, "1 item(s), 2 change impact operation(s)");
}

#[tokio::test]
async fn sandboxing_and_errors() {
    let cases = [
        (
            "js throw",
            "javascript",
            r#"ctx.impactNode("REQ-1", "x"); throw new Error("boom")"#,
            None,
        ),
        (
            "js timeout",
            "javascript",
            "while (true) {}",
            Some(Duration::from_millis(200)),
        ),
        ("js no fs", "javascript", r#"require("fs")"#, None),
        ("go script", "go", "package action", None),
        ("unknown language", "python", "print(1)", None),
    ];
    for (name, lang, code, timeout) in cases {
        let j = Job {
            language: lang.into(),
            code: code.into(),
            timeout: timeout.unwrap_or_default(),
            ..Default::default()
        };
        let (res, err) = run(&bg(), j, &FakeHost::default()).await;
        assert!(err.is_err(), "{name}: expected an error");
        assert!(
            res.items.is_empty() && res.nodes.is_empty(),
            "{name}: writes must be discarded on error"
        );
    }
}

#[tokio::test]
async fn timeout_interrupts_the_script() {
    let j = Job {
        language: "javascript".into(),
        code: "ctx.log('started'); while (true) {}".into(),
        timeout: Duration::from_millis(100),
        ..Default::default()
    };
    let started = std::time::Instant::now();
    let (res, err) = run(&bg(), j, &FakeHost::default()).await;
    let err = err.unwrap_err();
    assert!(err.is_interrupted(), "{err}");
    assert_eq!(
        err.to_string(),
        "script interrupted: context deadline exceeded"
    );
    assert!(started.elapsed() < Duration::from_secs(5));
    assert_eq!(
        res.logs.len(),
        1,
        "the logs of an interrupted script stay: {:?}",
        res.logs
    );
}

#[tokio::test]
async fn go_scripts_are_refused() {
    let j = Job {
        language: "go".into(),
        code: "package action".into(),
        ..Default::default()
    };
    let (_, err) = run(&bg(), j, &FakeHost::default()).await;
    let err = err.unwrap_err();
    assert!(matches!(err, Error::GoUnsupported));
    assert_eq!(
        err.to_string(),
        "the go script language is no longer supported: rewrite the script in javascript"
    );
}

#[tokio::test]
async fn suspension() {
    let j = Job {
        language: "javascript".into(),
        code: r#"ctx.impactNode("REQ-1", "x"); ctx.wakeOn("ping", "pong"); ctx.runAgent("slow", "do it")"#.into(),
        ..Default::default()
    };
    let (res, err) = run(&bg(), j, &FakeHost::default()).await;
    assert!(
        err.is_ok() && res.suspended && res.items.is_empty() && res.nodes.is_empty(),
        "expected suspension without writes, got {res:?} {err:?}"
    );
    assert_eq!(res.wake_on, ["ping", "pong"]);
}

/// `TestGoChangeImpacts`, the script in JavaScript.
#[tokio::test]
async fn change_impacts() {
    let j = Job {
        language: "javascript".into(),
        nodes: vec![ChangeImpact {
            key: "REQ-1".into(),
            planned: true,
            ..Default::default()
        }],
        code: r#"
for (const n of ctx.changeImpacts()) {
  if (n.planned) ctx.writeNode(n.key, { props: { title: "x" } });
}
const r = ctx.impactNodeCreate("TestCase", "TST-1", "cover");
ctx.writeNode(r, { links: [{ type: "verifies", to: "REQ-1" }] });
ctx.impactNodeReview(r, true, "ok");
ctx.impactNodeTransition(r, "approved");
ctx.impactNodeCancel("REQ-1");
"#
        .into(),
        ..Default::default()
    };
    let (res, err) = run(&bg(), j, &FakeHost::default()).await;
    err.unwrap();
    let n = &res.nodes;
    assert!(
        n.len() == 6
            && n[0].op == "write"
            && n[0].props["title"] == "x"
            && n[2].links[0].to == "REQ-1"
            && n[3].comment == "ok"
            && n[3].accept
            && n[4].op == "transition"
            && n[4].state == "approved"
            && n[5].op == "cancel",
        "unexpected operations {n:?}"
    );
}

#[tokio::test]
async fn decisions_vars_and_output() {
    let j = Job {
        language: "javascript".into(),
        action: "decider".into(),
        params: json!({"p": 3}).as_object().cloned().unwrap(),
        code: r#"
const d = ctx.openDecision("which?", { options: ["a", "b"], threshold: 0.8 });
ctx.decide(d, "a", 1, "because");
ctx.undecidable("", "unclear", ["why?"]);
ctx.answer("q1", "yes");
ctx.setVar("seen", ctx.param("p") + 1);
const s = ctx.signal("done", { k: 1 }, ctx.parentID());
const m = ctx.impactNodeMerge(["A", "B"], "T", "C", "merge");
const parts = ctx.impactNodeSplit("C", [{ type: "T", key: "C1", rationale: "split" }, { type: "T", key: "C2" }]);
ctx.setOutput({ d: d, s: s, m: m, parts: parts });
"#
        .into(),
        ..Default::default()
    };
    let (res, err) = run(&bg(), j, &FakeHost::default()).await;
    err.unwrap();
    assert_eq!(res.items.len(), 5);
    assert_eq!(
        Value::Object(res.items[0].clone()),
        json!({"type": "decisionPoint", "ref": "p1", "producedBy": "decider",
               "decisionPoint": {"op": "open", "ref": "#d1", "question": "which?", "options": ["a", "b"], "policy": {"threshold": 0.8}}})
    );
    assert_eq!(res.items[1]["decisionPoint"]["confidence"], json!(1));
    assert_eq!(res.items[2]["decisionPoint"]["questions"], json!(["why?"]));
    assert_eq!(res.items[4]["recipient"], "");
    assert_eq!(res.vars_set["seen"], json!(4));
    assert_eq!(res.nodes[0].sources, ["A", "B"]);
    assert_eq!(res.nodes[0].into[0].r#ref, "#n1");
    assert_eq!(res.nodes[1].rationale, "split");
    assert_eq!(res.output, "map[d:#d1 m:#n1 parts:[#n2 #n3] s:#p5]");
}

#[tokio::test]
async fn host_failures_throw() {
    struct Failing;
    #[async_trait]
    impl Host for Failing {
        async fn complete(
            &self,
            _: &goap_ctx::Ctx,
            _: CompleteRequest,
        ) -> Result<CompleteResult, Error> {
            Err(Error::host("no model"))
        }
        async fn run_agent(
            &self,
            _: &goap_ctx::Ctx,
            _: &str,
            _: &str,
        ) -> Result<AgentResult, Error> {
            Err(Error::host("no agent"))
        }
        async fn call_tool(
            &self,
            _: &goap_ctx::Ctx,
            _: &str,
            _: Map<String, Value>,
        ) -> Result<Value, Error> {
            Err(Error::host("no tool"))
        }
        async fn node(&self, _: &goap_ctx::Ctx, key: &str) -> Result<Node, Error> {
            Err(Error::host(format!("no node {key}")))
        }
        async fn nodes(&self, _: &goap_ctx::Ctx, _: &str) -> Result<Vec<Node>, Error> {
            Ok(vec![])
        }
        async fn links(
            &self,
            _: &goap_ctx::Ctx,
            _: &str,
            _: &str,
            _: &str,
        ) -> Result<Vec<Link>, Error> {
            Ok(vec![])
        }
    }
    let j = Job {
        language: "javascript".into(),
        code: r#"try { ctx.node("X") } catch (e) { ctx.log(String(e)) }
ctx.llm("hi")"#
            .into(),
        ..Default::default()
    };
    let (res, err) = run(&bg(), j, &Failing).await;
    assert_eq!(err.unwrap_err().to_string(), "Error: no model");
    assert_eq!(res.logs[0].message, "Error: no node X");
}

#[tokio::test]
async fn parallel_runs_of_one_script() {
    let host = Arc::new(FakeHost::default());
    let mut tasks = Vec::new();
    for i in 0..8 {
        let host = host.clone();
        tasks.push(tokio::spawn(async move {
            let j = Job {
                language: "javascript".into(),
                params: json!({"i": i}).as_object().cloned().unwrap(),
                code: "function run(ctx) { const r = ctx.complete({prompt: 'p'}); return ctx.param('i') + ':' + r.model }".into(),
                ..Default::default()
            };
            run(&goap_ctx::Ctx::background(), j, host.as_ref()).await
        }));
    }
    for (i, t) in tasks.into_iter().enumerate() {
        let (res, err) = t.await.unwrap();
        err.unwrap();
        assert_eq!(res.output, format!("{i}:default"));
    }
}

#[test]
fn job_json() {
    let j = Job {
        language: "javascript".into(),
        timeout: Duration::from_millis(200),
        ..Default::default()
    };
    let v = serde_json::to_value(&j).unwrap();
    assert_eq!(v["timeout"], json!(200_000_000));
    assert!(v.get("parentId").is_none());
    let back: Job =
        serde_json::from_value(json!({"language": "js", "items": null, "timeout": 5})).unwrap();
    assert_eq!(back.timeout, Duration::from_nanos(5));
}
