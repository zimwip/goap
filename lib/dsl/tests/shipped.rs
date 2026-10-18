//! The JavaScript rewrites of the Go scripts the data shipped (`not-blank` of `domains/alm.yaml`, `design_tests` of
//! `methodologies/examples/test-design.yaml`) behave as the Go ones did.

use async_trait::async_trait;
use goap_algo::{Bound, USAGE_PROPERTY_VALIDATOR};
use goap_dsl::{
    AgentResult, AlgorithmInput, ChangeImpact, CompleteRequest, CompleteResult, Error, Host, Job,
    Link, Node, run, run_algorithm,
};
use serde_json::{Map, Value, json};

fn yaml(path: &str) -> Value {
    let text =
        std::fs::read_to_string(format!("{}/../../{path}", env!("CARGO_MANIFEST_DIR"))).unwrap();
    serde_yaml_ng::from_str(&text).unwrap()
}

fn named<'a>(list: &'a Value, name: &str) -> &'a Value {
    list.as_array()
        .unwrap()
        .iter()
        .find(|x| x["name"] == name)
        .unwrap()
}

#[tokio::test]
async fn alm_not_blank() {
    let alm = yaml("domains/alm.yaml");
    let a = named(&alm["algorithms"], "not-blank");
    assert_eq!(a["language"], "javascript");
    let b = Bound {
        instance: "title-required".into(),
        r#type: USAGE_PROPERTY_VALIDATOR,
        language: "javascript".into(),
        code: a["code"].as_str().unwrap().into(),
        ..Default::default()
    };
    for (v, ok) in [
        (json!("x"), true),
        (json!(" \t"), false),
        (Value::Null, false),
        (json!(1), false),
    ] {
        let input = AlgorithmInput {
            property: "title".into(),
            value: v.clone(),
            ..Default::default()
        };
        let out = run_algorithm(&goap_ctx::Ctx::background(), &b, input)
            .await
            .unwrap();
        assert_eq!(out.ok(), ok, "{v}");
        if !ok {
            assert_eq!(out.failures, ["title must not be blank"]);
        }
    }
}

struct NoHost;

#[async_trait]
impl Host for NoHost {
    async fn complete(
        &self,
        _: &goap_ctx::Ctx,
        _: CompleteRequest,
    ) -> Result<CompleteResult, Error> {
        Err(Error::host("none"))
    }
    async fn run_agent(&self, _: &goap_ctx::Ctx, _: &str, _: &str) -> Result<AgentResult, Error> {
        Err(Error::host("none"))
    }
    async fn call_tool(
        &self,
        _: &goap_ctx::Ctx,
        _: &str,
        _: Map<String, Value>,
    ) -> Result<Value, Error> {
        Err(Error::host("none"))
    }
    async fn node(&self, _: &goap_ctx::Ctx, _: &str) -> Result<Node, Error> {
        Err(Error::host("none"))
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

#[tokio::test]
async fn test_design_design_tests() {
    let m = yaml("methodologies/examples/test-design.yaml");
    let a = named(&m["actions"], "design_tests");
    let req = |key: &str, props: Value| ChangeImpact {
        key: key.into(),
        r#type: "alm@Requirement".into(),
        intent: "modified".into(),
        pre: Some(Node {
            key: key.into(),
            props: props.as_object().cloned().unwrap_or_default(),
            ..Default::default()
        }),
        ..Default::default()
    };
    let job = Job {
        language: a["language"].as_str().unwrap().into(),
        code: a["code"].as_str().unwrap().into(),
        action: "design_tests".into(),
        nodes: vec![
            req("REQ-1", json!({"title": "Pay"})),
            req("REQ-2", Value::Null),
            ChangeImpact {
                key: "TC-0".into(),
                r#type: "alm@TestCase".into(),
                intent: "modified".into(),
                ..Default::default()
            },
        ],
        ..Default::default()
    };
    let (res, err) = run(&goap_ctx::Ctx::background(), job, &NoHost).await;
    err.unwrap();
    let ops: Vec<_> = res
        .nodes
        .iter()
        .map(|o| (o.op.as_str(), o.key.as_str(), o.props.get("title").cloned()))
        .collect();
    assert_eq!(
        ops,
        [
            ("declare", "TC-REQ-1", None),
            ("write", "", Some(json!("Verify REQ-1: Pay"))),
            ("declare", "TC-REQ-2", None),
            ("write", "", Some(json!("Verify REQ-2: <nil>"))),
        ]
    );
    assert_eq!(res.nodes[1].links[0].r#type, "alm@verifies");
    assert_eq!(res.nodes[1].links[0].to, "REQ-1");
    assert_eq!(res.output, "0 item(s), 4 change impact operation(s)");
}
