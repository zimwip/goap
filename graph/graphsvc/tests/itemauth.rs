mod common;

use std::collections::BTreeMap;
use std::sync::Arc;

use common::{casbin, graph};
use goap_authz::{Principal, system, with_principal};
use goap_connect::Request;
use goap_criticality::{self as criticality, Level};
use goap_ctx::Ctx;
use goap_domain::{Change, ChangeItem, DATA_CRITICALITY, item_permission_of};
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graphsvc::{Handler, item_authorizer};
use goap_risk as risk;

fn who(subject: &str, roles: &[&str]) -> Principal {
    Principal {
        subject: subject.into(),
        roles: roles.iter().map(|r| r.to_string()).collect(),
        ..Default::default()
    }
}

fn derogation(data: serde_json::Map<String, serde_json::Value>) -> ChangeItem {
    ChangeItem {
        r#type: risk::TYPE_DEROGATION,
        data,
        ..Default::default()
    }
}

/// Writing a derogation asks derogation:sign and that the signatory is the writer (ADR 0075 §2).
#[tokio::test]
async fn item_authorizer_signs() {
    risk::register();
    let perm = item_permission_of(&risk::TYPE_DEROGATION).unwrap();
    let item = |signatory: &str| derogation(obj! {"signatory" => signatory});
    let check = item_authorizer(Some(casbin()), None);
    let change = Change {
        id: "c".into(),
        ..Default::default()
    };
    for (name, p, signatory, denied) in [
        ("admin signs", who("alice", &["admin"]), "alice", false),
        (
            "role signs",
            who("bob", &["derogation_signatory"]),
            "bob",
            false,
        ),
        (
            "member does not",
            who("carol", &["developer"]),
            "carol",
            true,
        ),
        (
            "no one signs for one",
            who("alice", &["admin"]),
            "bob",
            true,
        ),
        (
            "the platform expires",
            system("derogation", &[]),
            "bob",
            false,
        ),
        ("anonymous does not", Principal::default(), "", true),
    ] {
        let ctx = with_principal(&Ctx::background(), p);
        let err = check
            .authorize(&ctx, &change, &item(signatory), &perm)
            .await;
        assert_eq!(
            err.as_ref().is_err_and(goap_rpcerr::is_forbidden),
            denied,
            "{name}: {err:?}"
        );
    }
    item_authorizer(None, None)
        .authorize(&Ctx::background(), &Change::default(), &item("x"), &perm)
        .await
        .expect("no authorizer");
}

/// A derogation also needs the role its criticality level asks of a signatory (ADR 0075 §3): derogation:sign-role
/// with the role of the policy resolved for the change.
#[tokio::test]
async fn item_authorizer_signatory_role() {
    risk::register();
    let perm = item_permission_of(&risk::TYPE_DEROGATION).unwrap();
    let resolve = |_: &Ctx, _: &Change, l: &Level| criticality::Policy {
        signatory_role: BTreeMap::from([(criticality::C3, "ciso")])
            .get(l)
            .map(|r| r.to_string())
            .unwrap_or_default(),
        ..Default::default()
    };
    let check = item_authorizer(Some(casbin()), Some(Arc::new(resolve)));
    let change = |l: &str| Change {
        id: "c".into(),
        data: obj! {DATA_CRITICALITY => l},
        ..Default::default()
    };
    let signer = who("bob", &["derogation_signatory"]);
    let item = derogation(obj! {"signatory" => "bob"});
    let closing = derogation(obj! {"signatory" => "bob", "status" => "closed"});
    for (name, p, c, it, denied) in [
        (
            "no role asked at C2",
            signer.clone(),
            change("C2"),
            &item,
            false,
        ),
        (
            "C3 asks ciso, a signer is not",
            signer.clone(),
            change("C3"),
            &item,
            true,
        ),
        (
            "C3 ciso signs",
            who("bob", &["derogation_signatory", "ciso"]),
            change("C3"),
            &item,
            false,
        ),
        (
            "an admin signs at C3",
            who("bob", &["admin"]),
            change("C3"),
            &item,
            false,
        ),
        (
            "closing asks no role",
            signer.clone(),
            change("C3"),
            &closing,
            false,
        ),
    ] {
        let ctx = with_principal(&Ctx::background(), p);
        let err = check.authorize(&ctx, &c, it, &perm).await;
        assert_eq!(
            err.as_ref().is_err_and(goap_rpcerr::is_forbidden),
            denied,
            "{name}: {err:?}"
        );
    }
}

/// The item types are the ones the domains in force declare, listed with their domain and the permission they ask
/// (ADR 0128).
#[tokio::test]
async fn list_item_types() {
    risk::register();
    let h = Handler::new(Arc::new(graph()));
    let r = h
        .list_item_types(
            &Ctx::background(),
            Request::new(pb::ListItemTypesRequest::default()),
        )
        .await
        .unwrap();
    let got: BTreeMap<String, pb::ItemTypeInfo> = r
        .msg
        .types
        .into_iter()
        .map(|k| (k.r#type.clone(), k))
        .collect();
    let k = got.get("artifact");
    assert!(
        matches!(k, Some(k) if k.domain == "methodology" && k.code && !k.description.is_empty()),
        "artifact: {k:?}"
    );
    let k = got.get("derogation");
    assert!(
        matches!(k, Some(k) if k.domain == "methodology" && k.permission == risk::PERMISSION_SIGN && k.subject_field == "signatory"),
        "derogation: {k:?}"
    );
}
