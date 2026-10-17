//! Port of `lib/dsl/algo_test.go`. The Go halves of the Go suite (`TestValidatorGo`, the Go scripts of
//! `TestTransitionActionBoth` and `TestLinkValidatorBoth`) are JavaScript here, and Go algorithms are refused.

use goap_algo::{
    Bound, USAGE_ACTION, USAGE_LINK_VALIDATOR, USAGE_PROPERTY_VALIDATOR, USAGE_TRANSITION_ACTION,
    USAGE_TRANSITION_GUARD, Usage,
};
use goap_dsl::{
    AlgorithmInput, Error, LinkInfo, Node, TransitionInfo, check_algorithm_code, run_algorithm,
};
use serde_json::{Value, json};

fn bound(t: Usage, lang: &str, code: &str, params: Value) -> Bound {
    Bound {
        instance: "i".into(),
        algorithm: "a".into(),
        r#type: t,
        language: lang.into(),
        code: code.into(),
        params: params.as_object().cloned().unwrap_or_default(),
        ..Default::default()
    }
}

fn bg() -> goap_ctx::Ctx {
    goap_ctx::Ctx::background()
}

#[tokio::test]
async fn validator_js() {
    let b = bound(
        USAGE_PROPERTY_VALIDATOR,
        "javascript",
        r#"
		var re = new RegExp(ctx.param("pattern"));
		if (!re.test(String(ctx.value()))) ctx.fail(ctx.property() + " does not match " + ctx.param("pattern"));"#,
        json!({"pattern": "^REQ-[0-9]+$"}),
    );
    let ok = AlgorithmInput {
        property: "id".into(),
        value: "REQ-12".into(),
        ..Default::default()
    };
    let out = run_algorithm(&bg(), &b, ok).await.unwrap();
    assert!(out.ok(), "valid value rejected: {:?}", out.failures);
    let bad = AlgorithmInput {
        property: "id".into(),
        value: "x".into(),
        ..Default::default()
    };
    let out = run_algorithm(&bg(), &b, bad).await.unwrap();
    assert!(
        !out.ok() && out.failures[0].contains("does not match"),
        "invalid value accepted: {:?}",
        out.failures
    );
}

#[tokio::test]
async fn validator_js_return() {
    let b = bound(
        USAGE_PROPERTY_VALIDATOR,
        "js",
        r#"return ctx.value() ? "" : "required""#,
        Value::Null,
    );
    let out = run_algorithm(&bg(), &b, AlgorithmInput::default())
        .await
        .unwrap();
    assert_eq!(out.failures, ["required"]);
    let out = run_algorithm(
        &bg(),
        &b,
        AlgorithmInput {
            value: "x".into(),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert!(out.ok(), "{:?}", out.failures);
    let b = bound(USAGE_PROPERTY_VALIDATOR, "js", "return false", Value::Null);
    let out = run_algorithm(&bg(), &b, AlgorithmInput::default())
        .await
        .unwrap();
    assert_eq!(out.failures, ["rejected"]);
}

/// `TestValidatorGo`, the script in JavaScript (the `not-blank` of the alm domain).
#[tokio::test]
async fn validator_not_blank() {
    let code = r#"
const v = ctx.value();
if (typeof v !== "string" || v.trim() === "") {
  ctx.fail(ctx.property() + " must not be blank");
}"#;
    let b = bound(USAGE_PROPERTY_VALIDATOR, "javascript", code, Value::Null);
    for (value, ok) in [
        (json!("aaa"), true),
        (json!("  "), false),
        (Value::Null, false),
        (json!(3), false),
    ] {
        let out = run_algorithm(
            &bg(),
            &b,
            AlgorithmInput {
                property: "p".into(),
                value: value.clone(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
        assert_eq!(out.ok(), ok, "{value}: {:?}", out.failures);
        if !ok {
            assert_eq!(out.failures, ["p must not be blank"]);
        }
    }
}

#[tokio::test]
async fn guard_js() {
    let b = bound(
        USAGE_TRANSITION_GUARD,
        "javascript",
        r#"
		for (const c of ctx.children()) if (c.state !== ctx.param("state")) ctx.fail(c.key + " is " + c.state);"#,
        json!({"state": "approved"}),
    );
    let child = |k: &str, s: &str| Node {
        key: k.into(),
        state: s.into(),
        ..Default::default()
    };
    let input = AlgorithmInput {
        children: vec![child("a", "approved"), child("b", "draft")],
        transition: TransitionInfo {
            name: "release".into(),
            ..Default::default()
        },
        ..Default::default()
    };
    let out = run_algorithm(&bg(), &b, input).await.unwrap();
    assert_eq!(out.failures, ["b is draft"]);
}

#[tokio::test]
async fn transition_action() {
    let js = bound(
        USAGE_TRANSITION_ACTION,
        "javascript",
        r#"ctx.setProp("approvedBy", ctx.param("who")); ctx.removeProp("draftNote")"#,
        json!({"who": "bot"}),
    );
    let out = run_algorithm(&bg(), &js, AlgorithmInput::default())
        .await
        .unwrap();
    assert!(
        out.set["approvedBy"] == "bot" && out.unset.len() == 1,
        "{out:?}"
    );
    // the Go half of TestTransitionActionBoth, in JavaScript
    let stamp = bound(
        USAGE_TRANSITION_ACTION,
        "javascript",
        r#"ctx.setProp("state", ctx.transition().to)"#,
        Value::Null,
    );
    let input = AlgorithmInput {
        transition: TransitionInfo {
            to: "approved".into(),
            ..Default::default()
        },
        ..Default::default()
    };
    let out = run_algorithm(&bg(), &stamp, input).await.unwrap();
    assert_eq!(out.set["state"], "approved");
    let both = bound(
        USAGE_TRANSITION_ACTION,
        "javascript",
        r#"ctx.removeProp("x"); ctx.setProp("x", 1); ctx.setProp("y", 2); ctx.removeProp("y")"#,
        Value::Null,
    );
    let out = run_algorithm(&bg(), &both, AlgorithmInput::default())
        .await
        .unwrap();
    assert_eq!(Value::Object(out.set), json!({"x": 1}));
    assert_eq!(out.unset, ["y"]);
}

#[tokio::test]
async fn algorithm_errors() {
    let err = run_algorithm(
        &bg(),
        &bound(
            USAGE_PROPERTY_VALIDATOR,
            "js",
            r#"throw new Error("boom")"#,
            Value::Null,
        ),
        AlgorithmInput::default(),
    )
    .await
    .unwrap_err();
    assert_eq!(err.to_string(), "algorithm i: Error: boom");
    let err = run_algorithm(
        &bg(),
        &bound(USAGE_ACTION, "js", "1", Value::Null),
        AlgorithmInput::default(),
    )
    .await
    .unwrap_err();
    assert_eq!(
        err.to_string(),
        r#"algorithm i: type "action" cannot be run"#
    );
    assert!(
        check_algorithm_code("js", "function (").is_err(),
        "syntax error expected"
    );
    assert!(check_algorithm_code("js", "return ctx.value() !== ''").is_ok());
    let err = check_algorithm_code("go", "package x\nfunc Run() error { return nil }").unwrap_err();
    assert!(matches!(err, Error::GoUnsupported), "{err}");
    let err = run_algorithm(
        &bg(),
        &bound(USAGE_PROPERTY_VALIDATOR, "go", "package v", Value::Null),
        AlgorithmInput::default(),
    )
    .await
    .unwrap_err();
    assert_eq!(
        err.to_string(),
        "algorithm i: the go script language is no longer supported: rewrite the script in javascript"
    );
    assert!(check_algorithm_code("lua", "x").is_err());
}

#[tokio::test]
async fn algorithm_timeout() {
    let (ctx, _cancel) = bg().with_timeout(std::time::Duration::from_millis(100));
    let err = run_algorithm(
        &ctx,
        &bound(USAGE_PROPERTY_VALIDATOR, "js", "for (;;) {}", Value::Null),
        AlgorithmInput::default(),
    )
    .await
    .unwrap_err();
    assert!(err.is_interrupted(), "{err}");
    assert_eq!(
        err.to_string(),
        "algorithm i: script interrupted: context deadline exceeded"
    );
}

#[tokio::test]
async fn link_validator() {
    let mut input = AlgorithmInput {
        link: LinkInfo {
            r#type: "d@refs".into(),
            props: json!({"weight": 3}).as_object().cloned().unwrap(),
        },
        from: Node {
            key: "A".into(),
            props: json!({"colour": "red"}).as_object().cloned().unwrap(),
            ..Default::default()
        },
        to: Node {
            key: "B".into(),
            props: json!({"colour": "blue"}).as_object().cloned().unwrap(),
            ..Default::default()
        },
        ..Default::default()
    };
    let js = bound(
        USAGE_LINK_VALIDATOR,
        "javascript",
        r#"
		if (ctx.from().props.colour !== ctx.to().props.colour) ctx.fail(ctx.link().type + " " + ctx.from().key + "->" + ctx.to().key + " w" + ctx.link().props.weight + " " + ctx.param("p"));"#,
        json!({"p": "x"}),
    );
    let out = run_algorithm(&bg(), &js, input.clone()).await.unwrap();
    assert_eq!(out.failures, ["d@refs A->B w3 x"]);
    // the Go half of TestLinkValidatorBoth, in JavaScript
    let other = bound(
        USAGE_LINK_VALIDATOR,
        "javascript",
        r#"if (ctx.from().props["colour"] !== ctx.to().props["colour"]) ctx.fail(ctx.link().type + " " + ctx.from().key + "->" + ctx.to().key)"#,
        Value::Null,
    );
    let out = run_algorithm(&bg(), &other, input.clone()).await.unwrap();
    assert_eq!(out.failures, ["d@refs A->B"]);
    input.to.props.insert("colour".into(), "red".into());
    for b in [&js, &other] {
        let out = run_algorithm(&bg(), b, input.clone()).await.unwrap();
        assert!(out.ok(), "accepted link rejected: {:?}", out.failures);
    }
}

#[tokio::test]
async fn usage_methods_only() {
    // a node validator has no value(); every algorithm context has instance() and algorithm()
    let b = bound(
        USAGE_PROPERTY_VALIDATOR,
        "js",
        r#"ctx.log(ctx.instance() + "/" + ctx.algorithm()); if (typeof ctx.children !== "undefined") ctx.fail("children")"#,
        Value::Null,
    );
    let out = run_algorithm(&bg(), &b, AlgorithmInput::default())
        .await
        .unwrap();
    assert!(out.ok(), "{:?}", out.failures);
    assert_eq!(out.logs[0].message, "i/a");
}
