//! Port of `operation_test.go`.

mod common;

use std::collections::BTreeMap;
use std::sync::Arc;

use common::casbin;
use goap_authz::{Principal, system, with_principal};
use goap_ctx::Ctx;
use goap_domain::Change;
use goap_graphsvc::{OperationRoles, operation_authorizer};
use goap_operations as operations;

/// The roles of each methodology, with the operations each role lists.
type Methods = BTreeMap<&'static str, Vec<(&'static str, Vec<&'static str>)>>;

/// The roles of a methodology say who performs each operation on its changes (ADR 0103): a reviewer accepts or
/// rejects impacts and never withdraws or edits one; a methodology listing no operation restricts nothing.
#[tokio::test]
async fn operation_authorizer_follows_the_roles() {
    // the roles of each methodology and the operations they list (none: every operation), as the registry resolves
    // them (Methodology::operation_roles)
    let methods: Arc<Methods> = Arc::new(BTreeMap::from([
        (
            "restricted",
            vec![
                ("author", vec!["doer"]),
                ("reviewer", vec!["reviewer"]),
                ("lead", vec![]),
            ],
        ),
        ("open", vec![("author", vec![])]),
    ]));
    let resolve: OperationRoles = Arc::new(move |_, name, op| {
        let methods = methods.clone();
        let (name, op) = (name.to_string(), op.to_string());
        Box::pin(async move {
            let Some(rs) = methods.get(name.as_str()) else {
                return Ok((Vec::new(), false));
            };
            if !rs.iter().any(|(_, ops)| !ops.is_empty()) {
                return Ok((Vec::new(), false));
            }
            let roles = rs
                .iter()
                .filter(|(_, ops)| ops.is_empty() || operations::allows(ops, &op))
                .map(|(r, _)| r.to_string())
                .collect();
            Ok((roles, true))
        })
    });
    let who = |roles: &[&str]| Principal {
        subject: "u".into(),
        roles: roles.iter().map(|r| r.to_string()).collect(),
        ..Default::default()
    };
    let check = operation_authorizer(Some(casbin()), Some(resolve.clone()));
    for (name, p, methodology, op, denied) in [
        (
            "reviewer reviews",
            who(&["reviewer"]),
            "restricted",
            operations::IMPACT_REVIEW,
            false,
        ),
        (
            "reviewer does not edit",
            who(&["reviewer"]),
            "restricted",
            operations::IMPACT_UPDATE,
            true,
        ),
        (
            "reviewer does not withdraw",
            who(&["reviewer"]),
            "restricted",
            operations::IMPACT_WITHDRAW,
            true,
        ),
        (
            "author edits",
            who(&["author"]),
            "restricted",
            operations::IMPACT_UPDATE,
            false,
        ),
        (
            "author does not review",
            who(&["author"]),
            "restricted",
            operations::IMPACT_REVIEW,
            true,
        ),
        (
            "lead does both",
            who(&["lead"]),
            "restricted",
            operations::IMPACT_REVIEW,
            false,
        ),
        (
            "outsider does nothing",
            who(&["guest"]),
            "restricted",
            operations::CHANGE_COMMIT,
            true,
        ),
        (
            "admin does everything",
            who(&["admin"]),
            "restricted",
            operations::IMPACT_WITHDRAW,
            false,
        ),
        (
            "open methodology",
            who(&["guest"]),
            "open",
            operations::IMPACT_WITHDRAW,
            false,
        ),
        (
            "unknown methodology",
            who(&["guest"]),
            "nope",
            operations::IMPACT_WITHDRAW,
            false,
        ),
        (
            "the platform acts",
            system("seed", &[]),
            "restricted",
            operations::IMPACT_UPDATE,
            false,
        ),
    ] {
        let ctx = with_principal(&Ctx::background(), p);
        let c = Change {
            id: "c".into(),
            methodology: methodology.into(),
            ..Default::default()
        };
        let err = check.authorize(&ctx, &c, op).await;
        assert_eq!(
            err.as_ref().is_err_and(goap_rpcerr::is_forbidden),
            denied,
            "{name}: {err:?}"
        );
    }
    let c = Change {
        methodology: "restricted".into(),
        ..Default::default()
    };
    operation_authorizer(None, Some(resolve))
        .authorize(&Ctx::background(), &c, operations::IMPACT_UPDATE)
        .await
        .expect("no authorizer");
}
