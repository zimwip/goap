use std::sync::{Arc, Mutex};
use std::time::Duration;

use goap_connect::{ConnectError, Ctx, Interceptors, Request, Response};
use goap_gen::model::v1 as pb;

use super::*;

fn args(a: &[&str]) -> Vec<String> {
    a.iter().map(|s| s.to_string()).collect()
}

#[test]
fn parse_reads_the_flags_like_go() {
    let env = |k: &str| match k {
        "GOAP_TOKEN" => Some("tok".to_string()),
        _ => None,
    };
    let f = parse(
        &args(&[
            "-model", "fast", "--runs=3", "-v", "-timeout", "1h30m", "evals/", "-x",
        ]),
        env,
    )
    .unwrap();
    assert_eq!(f.url, "http://localhost:8080");
    assert_eq!(f.token, "tok");
    assert_eq!(f.model, "fast");
    assert_eq!(f.runs, 3);
    assert!(f.verbose);
    assert_eq!(f.timeout, Duration::from_secs(5400));
    assert_eq!(f.paths, args(&["evals/", "-x"]));

    assert!(parse(&args(&["-nope"]), |_| None).is_err());
    assert!(parse(&args(&["-runs"]), |_| None).is_err());
    assert!(parse(&args(&["-runs", "x"]), |_| None).is_err());
    assert_eq!(parse_duration("250ms"), Some(Duration::from_millis(250)));
    assert_eq!(parse_duration("1.5s"), Some(Duration::from_millis(1500)));
    assert_eq!(parse_duration("5"), None);
}

#[test]
fn print_aligns_and_details_the_failures() {
    let rep = Report {
        cases: vec![
            goap_eval::CaseReport {
                name: "a-long-case".into(),
                model: "m".into(),
                pass_rate: 1.0,
                passed: true,
                input_tokens: 10.0,
                tool_calls: 1.0,
                runs: vec![goap_eval::RunResult {
                    passed: true,
                    ..Default::default()
                }],
                ..Default::default()
            },
            goap_eval::CaseReport {
                name: "b".into(),
                model: "m".into(),
                runs: vec![goap_eval::RunResult {
                    passed: false,
                    trajectory: vec!["t1".into(), "t2".into()],
                    checks: vec![
                        goap_eval::Check {
                            name: "items".into(),
                            passed: false,
                            detail: "missing".into(),
                        },
                        goap_eval::Check {
                            name: "rounds".into(),
                            passed: true,
                            detail: String::new(),
                        },
                    ],
                    answer: "x".repeat(700),
                    ..Default::default()
                }],
                ..Default::default()
            },
        ],
        passed: 1,
        failed: 1,
    };
    let mut out = Vec::new();
    print(&mut out, &rep, false);
    let out = String::from_utf8(out).unwrap();
    let lines: Vec<&str> = out.lines().collect();
    assert_eq!(
        lines[0],
        "CASE         MODEL  PASS  RATE  IN TOK  OUT TOK  TOOLS  MS"
    );
    assert_eq!(
        lines[1],
        "a-long-case  m      ok    100%  10      0        1.0    0"
    );
    assert_eq!(
        lines[2],
        "b            m      FAIL  0%    0       0        0.0    0"
    );
    assert!(out.contains("\nb, run 1: passed=false trajectory=[t1 t2]\n  [x] items missing\n"));
    assert!(!out.contains("rounds"));
    assert!(out.contains(&format!("  answer: {}\n", "x".repeat(600))));
    assert!(!out.contains(&"x".repeat(601)));
    assert!(out.ends_with("\n1 passed, 1 failed\n"));
}

/// A gateway answering every completion with a fixed artifact, recording what it was sent.
#[derive(Default)]
struct FakeGateway {
    seen: Mutex<Vec<(String, pb::CompleteRequest)>>,
}

#[goap_connect::async_trait]
impl pb::ModelService for FakeGateway {
    async fn complete(
        &self,
        _ctx: &Ctx,
        req: Request<pb::CompleteRequest>,
    ) -> Result<Response<pb::CompleteResponse>, ConnectError> {
        let auth = req.header("authorization").to_string();
        self.seen.lock().unwrap().push((auth, req.into_inner()));
        Ok(Response::new(pb::CompleteResponse {
            text: r#"{"items":[{"type":"artifact","target":"note","data":{"text":"hi"}}]}"#.into(),
            provider: "fake".into(),
            model: "fake-1".into(),
            usage: Some(pb::Usage {
                input_tokens: 7,
                output_tokens: 3,
                ..Default::default()
            }),
            ..Default::default()
        }))
    }
}

const CASE: &str = r#"cases:
  - name: note
    prompt: engine
    messages:
      - role: user
        content: Register a note.
    json: true
    expect:
      items:
        - {type: artifact, target: note}
"#;

#[tokio::test]
async fn run_evaluates_through_the_gateway() {
    let gw = Arc::new(FakeGateway::default());
    let app = pb::model_service_router(gw.clone(), Interceptors::none());
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    let server = tokio::spawn(async move { axum::serve(ln, app).await });

    let dir = std::env::temp_dir().join(format!("goap-eval-cmd-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let case = dir.join("case.yaml");
    std::fs::write(&case, CASE).unwrap();
    let report = dir.join("report.json");

    let mut out = Vec::new();
    let mut err = Vec::new();
    let code = run(
        &args(&[
            "-url",
            &format!("{url}/"),
            "-token",
            "secret",
            "-model",
            "fast",
            "-json",
            report.to_str().unwrap(),
            case.to_str().unwrap(),
        ]),
        &mut out,
        &mut err,
    )
    .await;
    let out = String::from_utf8(out).unwrap();
    let stored = std::fs::read_to_string(&report).unwrap_or_default();
    let _ = std::fs::remove_dir_all(&dir);
    server.abort();

    assert_eq!(code, 0, "{out}\n{}", String::from_utf8_lossy(&err));
    assert!(out.contains("1 passed, 0 failed"), "{out}");
    let rep: serde_json::Value = serde_json::from_str(&stored).unwrap();
    assert_eq!(rep["passed"], 1);
    let seen = gw.seen.lock().unwrap();
    assert!(!seen.is_empty());
    let (auth, req) = &seen[0];
    assert_eq!(auth, "Bearer secret");
    assert_eq!(req.model, "fast");
    assert!(req.json);
    let meta = req.meta.as_ref().unwrap();
    assert_eq!(meta.source, "eval");
    assert_eq!(meta.conversation_id, "eval:note");
}

#[tokio::test]
async fn usage_and_load_errors_exit_2() {
    let mut out = Vec::new();
    let mut err = Vec::new();
    assert_eq!(run(&[], &mut out, &mut err).await, 2);
    assert!(String::from_utf8_lossy(&err).contains("usage: goap-eval"));
    let mut err = Vec::new();
    assert_eq!(
        run(
            &args(&["/nonexistent/goap-eval-case.yaml"]),
            &mut out,
            &mut err
        )
        .await,
        2
    );
    assert!(String::from_utf8_lossy(&err).starts_with("goap-eval: "));
}
