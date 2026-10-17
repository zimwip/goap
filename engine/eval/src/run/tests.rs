use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

use goap_ctx::Ctx;
use goap_llm as llm;
use parking_lot::Mutex;
use serde_json::json;

use super::*;
use crate::case::parse;

const CASE_YAML: &str = r#"
cases:
  - name: read-and-register
    prompt: engine
    system: Register the document you read.
    messages:
      - role: user
        content: Read spec.txt and register it.
    json: true
    runs: 2
    tools:
      - name: document-repository/read
        description: Read a document.
        inputSchema: {type: object, properties: {path: {type: string}}}
        error: not found
        responses:
          - args: {path: spec.txt}
            result: {text: the spec}
    expect:
      items:
        - {type: artifact, target: doc, data: {seen: true}}
      trajectory: {match: in_order, tools: [document-repository/read]}
      maxToolCalls: 2
      noToolErrors: true
    judge:
      rubric: The agent registered the document it read.
  - name: plain
    messages: [{role: user, content: say hello}]
    expect: {contains: [hello], notContains: [goodbye]}
"#;

#[test]
fn parse_checks_the_cases() {
    let cs = parse(CASE_YAML.as_bytes()).unwrap();
    assert!(
        cs.len() == 2
            && cs[0].tools[0].responses[0].args["path"] == "spec.txt"
            && cs[0].expect.trajectory.as_ref().unwrap().r#match == MATCH_IN_ORDER,
        "{cs:?}"
    );
    for (doc, want) in [
        ("messages: [{role: user, content: x}]", "a case has a name"),
        (
            "{name: a, messages: [{role: assistant, content: x}]}",
            "start with a user message",
        ),
        (
            "{name: a, prompt: other, messages: [{role: user, content: x}]}",
            "prompt",
        ),
        (
            r#"{name: a, messages: [{role: user, content: x}], judge: {rubric: " "}}"#,
            "rubric",
        ),
        (
            "{name: a, messages: [{role: user, content: x}], expect: {trajectory: {match: some, tools: []}}}",
            "trajectory match",
        ),
        (
            "cases: [{name: a, messages: [{role: user, content: x}]}, {name: a, messages: [{role: user, content: y}]}]",
            "twice",
        ),
    ] {
        match parse(doc.as_bytes()) {
            Err(e) if e.to_string().contains(want) => {}
            other => panic!("{doc}: {other:?}, want {want:?}"),
        }
    }
}

fn text(t: &str) -> llm::Response {
    llm::Response {
        text: t.into(),
        ..Default::default()
    }
}

#[tokio::test]
async fn run_scores_the_answer_the_trajectory_and_the_judge() {
    let cs = parse(CASE_YAML.as_bytes()).unwrap();
    let reqs: Arc<Mutex<Vec<llm::Request>>> = Arc::default();
    let bad_source: Arc<Mutex<Vec<String>>> = Arc::default();
    let (r2, b2) = (reqs.clone(), bad_source.clone());
    let client = llm::ClientFunc::shared(move |ctx: Ctx, r: llm::Request| {
        let (reqs, bad) = (r2.clone(), b2.clone());
        async move {
            reqs.lock().push(r.clone());
            let src = llm::meta_from(&ctx).source;
            if src != llm::SOURCE_EVAL {
                bad.lock().push(src);
            }
            if r.system.contains("You evaluate the answer") {
                return Ok(llm::Response {
                    usage: llm::Usage {
                        input_tokens: 5,
                        ..Default::default()
                    },
                    ..text(r#"{"score": 0.9, "reason": "it registered it"}"#)
                });
            }
            if r.messages.len() == 1 {
                return Ok(llm::Response {
                    tool_calls: vec![llm::ToolCall {
                        id: "c1".into(),
                        name: "document-repository/read".into(),
                        arguments: json!({"path": "spec.txt"}).as_object().cloned().unwrap(),
                    }],
                    usage: llm::Usage {
                        input_tokens: 100,
                        output_tokens: 10,
                        ..Default::default()
                    },
                    ..Default::default()
                });
            }
            Ok(llm::Response {
                usage: llm::Usage {
                    input_tokens: 120,
                    output_tokens: 20,
                    ..Default::default()
                },
                ..text(
                    r#"{"items":[{"type":"artifact","target":"doc","data":{"seen":true,"extra":1}}]}"#,
                )
            })
        }
    });
    let rep = Runner::new(client).run(&Ctx::background(), &cs[0]).await;
    assert!(
        bad_source.lock().is_empty(),
        "sources {:?}",
        bad_source.lock()
    );
    assert!(
        rep.passed && rep.pass_rate == 1.0 && rep.runs.len() == 2 && rep.tool_calls == 1.0,
        "report {rep:?}"
    );
    let run = &rep.runs[0];
    assert!(
        run.score == Some(0.9) && run.input_tokens == 225 && run.rounds == 1,
        "run {run:?}"
    );
    // the engine's system text comes first, the tools are given natively, the mocked answer goes back by call id
    let reqs = reqs.lock();
    let (first, second) = (&reqs[0], &reqs[1]);
    assert!(
        first
            .system
            .starts_with("You are an agent of an enterprise methodology platform")
            && first.system.ends_with("Register the document you read.")
            && first.tools.len() == 1
            && first.json,
        "first request {first:?}"
    );
    let tr = &second.messages[2].tool_results;
    assert!(
        tr.len() == 1
            && tr[0].call_id == "c1"
            && !tr[0].is_error
            && tr[0].content == r#"{"text":"the spec"}"#,
        "tool results {tr:?}"
    );
}

#[tokio::test]
async fn failed_checks_fail_the_run() {
    let cs = parse(CASE_YAML.as_bytes()).unwrap();
    let mut c = cs[0].clone();
    c.runs = 1;
    c.judge = None;
    // the model reads another path (the mocked error answers), then answers without the item
    let n = Arc::new(AtomicUsize::new(0));
    let n2 = n.clone();
    let client = llm::ClientFunc::shared(move |_: Ctx, _: llm::Request| {
        let n = n2.clone();
        async move {
            if n.fetch_add(1, Ordering::SeqCst) == 0 {
                return Ok(text(
                    r#"{"tool_calls":[{"name":"document-repository/read","arguments":{"path":"other"}},{"name":"ticketing/create","arguments":{}}]}"#,
                ));
            }
            Ok(text(r#"{"items":[]}"#))
        }
    });
    let runner = Runner::new(client);
    let rep = runner.run(&Ctx::background(), &c).await;
    let run = &rep.runs[0];
    let failed: Vec<&str> = run
        .checks
        .iter()
        .filter(|ch| !ch.passed)
        .map(|ch| ch.name.as_str())
        .collect();
    assert!(
        !rep.passed
            && failed.contains(&"item 1")
            && failed.contains(&"no tool errors")
            && !failed.contains(&"trajectory in_order")
            && run.tool_errors == 2
            && run.rounds == 1,
        "checks {:?} errors {}",
        run.checks,
        run.tool_errors
    );
    // an exact trajectory fails on the extra call, and so does the bound
    c.expect.trajectory.as_mut().unwrap().r#match = MATCH_EXACT.into();
    c.expect.max_tool_calls = 1;
    n.store(0, Ordering::SeqCst);
    let rep = runner.run(&Ctx::background(), &c).await;
    let run = &rep.runs[0];
    assert!(
        run.checks[2].name == "trajectory exact" && !run.checks[2].passed && !run.checks[3].passed,
        "checks {:?}",
        run.checks
    );
}

#[tokio::test]
async fn pass_rate_and_model_override() {
    let cs = parse(CASE_YAML.as_bytes()).unwrap();
    let mut c = cs[1].clone();
    c.runs = 4;
    c.min_pass_rate = 0.5;
    let n = Arc::new(AtomicUsize::new(0));
    let models: Arc<Mutex<Vec<String>>> = Arc::default();
    let (n2, m2) = (n.clone(), models.clone());
    let client = llm::ClientFunc::shared(move |_: Ctx, r: llm::Request| {
        let (n, models) = (n2.clone(), m2.clone());
        async move {
            let k = n.fetch_add(1, Ordering::SeqCst) + 1;
            models.lock().push(r.model.clone());
            if k % 2 == 0 {
                return Ok(text("goodbye"));
            }
            Ok(text("Hello there"))
        }
    });
    let mut runner = Runner::new(client);
    runner.model = "fast".into();
    let rep = runner.run_all(&Ctx::background(), &[c]).await;
    let models = models.lock();
    assert!(
        rep.passed == 1
            && rep.cases[0].pass_rate == 0.5
            && models[0] == "fast"
            && rep.cases[0].model == "fast",
        "report {rep:?} models {models:?}"
    );
}

#[test]
fn trajectory_matches_cases() {
    let got: Vec<String> = ["a", "x", "b", "a"].map(String::from).to_vec();
    for (m, tools, want) in [
        (MATCH_EXACT, vec!["a", "x", "b", "a"], true),
        (MATCH_EXACT, vec!["a", "b"], false),
        (MATCH_IN_ORDER, vec!["a", "b"], true),
        (MATCH_IN_ORDER, vec!["b", "x"], false),
        (MATCH_ANY_ORDER, vec!["b", "a", "a"], true),
        (MATCH_ANY_ORDER, vec!["a", "a", "a"], false),
    ] {
        let t = Trajectory {
            r#match: m.into(),
            tools: tools.iter().map(|s| s.to_string()).collect(),
        };
        assert_eq!(trajectory_matches(&t, &got), want, "{m} {tools:?}");
    }
}

#[test]
fn subset_cases() {
    let got = json!({"type": "artifact", "data": {"n": 3.0, "tags": ["a", "b"]}});
    for (want, ok) in [
        (r#"{"type": "artifact"}"#, true),
        (r#"{"data": {"n": 3}}"#, true),
        (r#"{"data": {"tags": ["b"]}}"#, true),
        (r#"{"data": {"tags": ["c"]}}"#, false),
        (r#"{"type": "decision"}"#, false),
        (r#"{"missing": "x"}"#, false),
    ] {
        let doc = format!(
            "{{name: a, messages: [{{role: user, content: x}}], expect: {{items: [{want}]}}}}"
        );
        let cs = parse(doc.as_bytes()).unwrap();
        let w = Value::Object(cs[0].expect.items[0].clone());
        assert_eq!(subset(&w, &got), ok, "{want}");
    }
}
