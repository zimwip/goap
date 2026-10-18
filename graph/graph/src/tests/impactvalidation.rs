//! The views of a change carry the validation of each draft (a read model). The
//! levels world comes from `validators_levels_test.go` (Go runs these tests on the memory store only, as here); its
//! link validator, a Go script there, is JavaScript here (the DSL has no Go language any more).

use std::sync::Arc;

use goap_domain::*;
use goap_graphapi::{LinkWrite, NodeCreate, NodeUpdate};
use serde_json::{Map, Value, json};

use crate::testsupport::*;
use crate::{Graph, NewChange, Result};

const LEVELS_DOMAIN: &str = r#"
name: lv
version: 1.0.0
algorithms:
  - {name: not-blank, type: property_validator, language: javascript, code: 'return String(ctx.value() || "").trim() !== "" ? "" : "title is blank"'}
  - {name: tag-needs-colour, type: node_validator, language: javascript, code: 'if (ctx.node().props.tag && !ctx.node().props.colour) ctx.fail("a tag needs a colour")'}
  - {name: positive, type: property_validator, language: javascript, code: 'if (ctx.value() !== undefined && ctx.value() !== null && Number(ctx.value()) <= 0) ctx.fail("weight must be positive")'}
  - {name: same-colour, type: link_validator, language: javascript, code: 'if (ctx.from().props.colour !== ctx.to().props.colour) ctx.fail("colours differ (" + ctx.link().type + ")")'}
algorithmInstances:
  - {name: title-required, algorithm: not-blank}
  - {name: tag-rule, algorithm: tag-needs-colour}
  - {name: weight-positive, algorithm: positive}
  - {name: colours, algorithm: same-colour}
nodeTypes:
  - name: Item
    validators: [tag-rule]
    attributes: [{name: title, validators: [title-required]}, tag, colour]
linkTypes:
  - name: refs
    from: Item
    to: Item
    validators: [colours]
    attributes: [{name: weight, type: number, validators: [weight-positive]}]
"#;

/// levelsGraph is judged by a domain plugging a validator at each level: an attribute (title), a node (a tag needs a
/// colour), an attribute of a link (weight) and a link (both ends of the same colour).
async fn levels_graph() -> (Graph, Baseline) {
    let d = goap_def::parse_domain(LEVELS_DOMAIN.as_bytes()).expect("domain");
    let cat = goap_typecat::new(&[d]).expect("catalogue");
    let mut g = memory_repo().graph();
    use_types(&mut g, Arc::new(cat));
    let b = must(g.branch_head(&ctx(), "lv", MAIN_BRANCH).await);
    (g, b)
}

struct LevelsChange {
    g: Graph,
    c: Change,
    a: ChangeImpact,
    b: ChangeImpact,
}

fn obj(v: Value) -> Map<String, Value> {
    v.as_object().cloned().unwrap_or_default()
}

/// newLevelsChange drafts A and B of one change and a link A -> B carrying link.
async fn new_levels_change(a: Value, b: Value, link: Value) -> LevelsChange {
    let ctx = ctx();
    let (g, base) = levels_graph().await;
    let c = must(
        g.create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                namespace: "lv".into(),
                title: "t".into(),
                baseline_id: base.id,
                ..Default::default()
            },
        )
        .await,
    );
    let item = |key: &str, p: Value| NodeCreate {
        rationale: "r".into(),
        key: key.into(),
        r#type: "lv@Item".into(),
        properties: obj(p),
        ..Default::default()
    };
    let b = must(g.impact_node_create(&ctx, &c.id, item("B", b)).await);
    let a = must(
        g.impact_node_create(
            &ctx,
            &c.id,
            NodeCreate {
                links: vec![LinkWrite {
                    r#type: "lv@refs".into(),
                    to: b.post.clone().unwrap(),
                    properties: obj(link),
                }],
                ..item("A", a)
            },
        )
        .await,
    );
    LevelsChange { g, c, a, b }
}

impl LevelsChange {
    async fn accept(&self, cn: &ChangeImpact) -> Result<ChangeImpact> {
        self.g
            .impact_node_review(&ctx(), &self.c.id, &cn.id, REVIEW_ACCEPTED, "tester", "ok")
            .await
    }

    async fn checked(&self) -> Vec<ChangeImpact> {
        must(self.g.change_checked(&ctx(), &self.c.id).await).nodes
    }

    async fn board(&self) -> Vec<ChangeImpact> {
        must(self.g.blackboard(&ctx(), &self.c.id).await)
            .change
            .nodes
    }

    async fn update(&self, p: Value) {
        must(
            self.g
                .impact_node_update(
                    &ctx(),
                    &self.c.id,
                    &self.a.id,
                    NodeUpdate {
                        properties: obj(p),
                        ..Default::default()
                    },
                )
                .await,
        );
    }
}

fn impact_by_key(nodes: &[ChangeImpact], key: &str) -> ChangeImpact {
    nodes
        .iter()
        .find(|cn| cn.key == key)
        .cloned()
        .unwrap_or_else(|| panic!("no impact {key} in {nodes:?}"))
}

// The views of a change carry the validation of each draft: the message an acceptance would give, valid once edited.
#[tokio::test]
async fn impact_validation_in_views() {
    let lc = new_levels_change(
        json!({"title": " ", "colour": "red"}),
        json!({"title": "t", "colour": "red"}),
        Value::Null,
    )
    .await;
    let accept_err = lc.accept(&lc.a).await.expect_err("A is invalid");
    let mut want = accept_err.message().to_string();
    while let Some(s) = want.strip_suffix(": invalid") {
        want = s.to_string();
    }
    for name in ["change", "blackboard"] {
        let view = if name == "change" {
            lc.checked().await
        } else {
            lc.board().await
        };
        let (a, b) = (impact_by_key(&view, "A"), impact_by_key(&view, "B"));
        assert!(
            a.validation
                .as_ref()
                .is_some_and(|v| !v.valid && v.message == want),
            "{name}: A = {:?}, want message {want:?}",
            a.validation
        );
        assert!(
            b.validation
                .as_ref()
                .is_some_and(|v| v.valid && v.message.is_empty()),
            "{name}: B = {:?}",
            b.validation
        );
    }
    // an edit fixing the draft changes the log: the next view is valid
    lc.update(json!({"title": "ok"})).await;
    for name in ["change", "blackboard"] {
        let view = if name == "change" {
            lc.checked().await
        } else {
            lc.board().await
        };
        let v = impact_by_key(&view, "A").validation;
        assert!(
            v.as_ref().is_some_and(|v| v.valid),
            "{name}: after the fix A = {v:?}"
        );
    }
    must(lc.accept(&lc.a).await);
    // and another edit breaks it again, the cache follows the log
    lc.update(json!({"tag": "x", "colour": ""})).await;
    let v = impact_by_key(&lc.checked().await, "A").validation;
    assert!(
        v.as_ref()
            .is_some_and(|v| !v.valid && v.message.contains("a tag needs a colour")),
        "after breaking: {v:?}"
    );
}

// The validation is a read model: nothing stored carries it, and an impact with no draft (a landed change) has none.
#[tokio::test]
async fn impact_validation_not_stored_and_absent_without_draft() {
    let lc = new_levels_change(
        json!({"title": "t", "colour": "red"}),
        json!({"title": "t", "colour": "red"}),
        Value::Null,
    )
    .await;
    for cn in lc.checked().await {
        assert!(
            cn.validation.is_some(),
            "{} has a draft and no validation",
            cn.key
        );
    }
    for cn in must(lc.g.list_change_impacts(&ctx(), &lc.c.id).await) {
        assert!(
            cn.validation.is_none(),
            "stored impact {} carries a validation",
            cn.key
        );
    }
    for cn in [&lc.b, &lc.a] {
        must(lc.accept(cn).await);
    }
    must(apply(&lc.g, &lc.c.id, "").await);
    for cn in lc.checked().await {
        assert!(
            cn.validation.is_none(),
            "landed impact {} has a validation {:?}",
            cn.key,
            cn.validation
        );
    }
}

// An impact the change withdrew holds no draft any more: no validation.
#[tokio::test]
async fn impact_validation_withdrawn() {
    let lc = new_levels_change(json!({"title": " "}), json!({"title": "t"}), Value::Null).await;
    must(
        lc.g.withdraw_impact(&ctx(), &lc.c.id, &lc.a.id, "", "")
            .await,
    );
    for cn in lc.checked().await {
        assert!(
            !(cn.key == "A" && cn.validation.is_some()),
            "withdrawn impact has a validation {:?}",
            cn.validation
        );
    }
}

// ---- validators_levels_test.go ----------------------------------------------------------------------------------

// The draft is refused at its review with the message, and accepted once valid.
#[tokio::test]
async fn validators_of_each_level_refuse_at_review() {
    let ok = json!({"title": "t", "colour": "red"});
    let cases = [
        (
            "attribute",
            json!({"title": " ", "colour": "red"}),
            ok.clone(),
            Value::Null,
            r#"property "title" is invalid: title is blank"#,
        ),
        (
            "node",
            json!({"title": "t", "tag": "x"}),
            ok.clone(),
            Value::Null,
            "a tag needs a colour",
        ),
        (
            "link attribute",
            ok.clone(),
            ok.clone(),
            json!({"weight": -1}),
            r#"link lv@refs: property "weight" is invalid: weight must be positive"#,
        ),
        (
            "link",
            ok.clone(),
            json!({"title": "t", "colour": "blue"}),
            Value::Null,
            "link lv@refs to B is invalid: colours differ (lv@refs) (validator colours)",
        ),
    ];
    for (name, a, b, link, want) in cases {
        let lc = new_levels_change(a, b, link).await;
        let b = lc.b.clone();
        let r = lc.accept(&b).await;
        assert!(r.is_ok(), "{name}: B is valid: {r:?}");
        let a = lc.a.clone();
        let err = lc.accept(&a).await.unwrap_err();
        assert!(
            err.is_invalid() && err.to_string().contains(want),
            "{name}: want {want:?}, got {err}"
        );
    }
}

// valid drafts are accepted and land; a link validator also judges the version written at landing.
#[tokio::test]
async fn validators_of_each_level_accept_valid() {
    let ok = json!({"title": "t", "colour": "red", "tag": "x"});
    let lc = new_levels_change(ok.clone(), ok, json!({"weight": 2})).await;
    for cn in [lc.b.clone(), lc.a.clone()] {
        must(lc.accept(&cn).await);
    }
    must(apply(&lc.g, &lc.c.id, "").await);
}

// A target changed after the source was accepted is caught when the version lands.
#[tokio::test]
async fn link_validator_runs_at_landing() {
    let ok = json!({"title": "t", "colour": "red"});
    let lc = new_levels_change(ok.clone(), ok, Value::Null).await;
    for cn in [lc.b.clone(), lc.a.clone()] {
        must(lc.accept(&cn).await);
    }
    // B turns blue and is accepted again: A, still accepted, no longer matches it
    must(
        lc.g.impact_node_update(
            &ctx(),
            &lc.c.id,
            &lc.b.id,
            NodeUpdate {
                properties: obj(json!({"colour": "blue"})),
                ..Default::default()
            },
        )
        .await,
    );
    let b = lc.b.clone();
    must(lc.accept(&b).await);
    let err = apply(&lc.g, &lc.c.id, "").await.unwrap_err();
    assert!(
        err.is_invalid() && err.to_string().contains("colours differ"),
        "landing: {err}"
    );
}

/// A resolver of the tests: every reference resolves to one node, or to none.
struct FixedResolver(Option<Node>);

#[async_trait::async_trait]
impl crate::algorithms::NodeResolver for FixedResolver {
    async fn resolve(&mut self, _: &goap_ctx::Ctx, _: &NodeRef) -> Result<Option<Node>> {
        Ok(self.0.clone())
    }
}

// Every level also judges a written version directly (checkWritten, what Apply runs).
#[tokio::test]
async fn check_version_runs_every_level() {
    let ctx = ctx();
    let (g, _) = levels_graph().await;
    let ix = crate::lifecycle::TypeIndex { cat: g.catalog() };
    let to = Node {
        id: NodeID::new("n2"),
        version: 1,
        key: "B".into(),
        r#type: "lv@Item".into(),
        properties: obj(json!({"colour": "blue"})),
        ..Default::default()
    };
    let link = |p: Value| {
        vec![Link {
            r#type: "lv@refs".into(),
            to: to.r#ref(),
            properties: obj(p),
            ..Default::default()
        }]
    };
    let item = |p: Value| Node {
        key: "A".into(),
        r#type: "lv@Item".into(),
        properties: obj(p),
        ..Default::default()
    };
    let mut n = Node {
        id: NodeID::new("n1"),
        version: 1,
        ..item(json!({"title": "t", "colour": "blue"}))
    };
    let mut resolve = FixedResolver(Some(to.clone()));
    must(
        g.check_version(&ctx, &ix, &n, &link(json!({"weight": 1})), &mut resolve)
            .await,
    );
    let cases = [
        (
            "attribute",
            item(json!({"title": ""})),
            vec![],
            "title is blank",
        ),
        (
            "node",
            item(json!({"tag": "x", "title": "t"})),
            vec![],
            "a tag needs a colour",
        ),
        (
            "link attribute",
            n.clone(),
            link(json!({"weight": 0})),
            "weight must be positive",
        ),
        (
            "link",
            Node {
                id: NodeID::new("n1"),
                ..item(json!({"title": "t", "colour": "red"}))
            },
            link(Value::Null),
            "colours differ",
        ),
    ];
    for (name, cn, l, want) in cases {
        let err = g
            .check_version(&ctx, &ix, &cn, &l, &mut resolve)
            .await
            .unwrap_err();
        assert!(
            err.is_invalid() && err.to_string().contains(want),
            "{name}: {err}"
        );
    }
    // a target that cannot be resolved is left to the other checks
    let mut gone = FixedResolver(None);
    n.properties.insert("colour".into(), json!("red"));
    must(
        g.check_version(&ctx, &ix, &n, &link(Value::Null), &mut gone)
            .await,
    );
}
