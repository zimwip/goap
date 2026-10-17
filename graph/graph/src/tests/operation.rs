//! The authorizer part of `operation_test.go`: every operation of the catalogue asks the OperationAuthorizer first
//! (ADR 0103), with the change it acts on; a reviewer may only review. (The operations themselves are later slices':
//! they call `allow` as their first step.)

use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{Change, ChangeID};
use goap_operations as operations;
use parking_lot::Mutex;

use crate::testsupport::*;
use crate::{Error, NewChange, OperationAuthorizer, Result};

struct Reviewer {
    change: ChangeID,
    asked: Mutex<Vec<String>>,
}

#[async_trait]
impl OperationAuthorizer for Reviewer {
    async fn authorize(&self, _: &Ctx, c: &Change, op: &str) -> Result<()> {
        assert_eq!(c.id, self.change, "asked for another change");
        self.asked.lock().push(op.to_string());
        match operations::lookup(op) {
            Some(o) if o.kind == operations::Kind::Reviewer => Ok(()),
            _ => Err(Error::msg("refused")),
        }
    }
}

#[tokio::test]
async fn operation_authorizer() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let f = new_fixture(r).await;
        let mut g = f.g;
        let ch = must(
            g.create_change(
                &c,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "rights".into(),
                    baseline_id: f.base.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        let a = Arc::new(Reviewer {
            change: ch.id.clone(),
            asked: Mutex::new(Vec::new()),
        });
        g.operation_authorizer = Some(a.clone());
        for op in [
            operations::IMPACT_UPDATE,
            operations::IMPACT_WITHDRAW,
            operations::IMPACT_CHECKOUT,
        ] {
            let e = g
                .allow(&c, &ch.id, op)
                .await
                .expect_err("a reviewer only reviews");
            assert_eq!(e.to_string(), "refused", "{op}");
        }
        must(g.allow(&c, &ch.id, operations::IMPACT_REVIEW).await);
        assert_eq!(
            *a.asked.lock(),
            vec![
                operations::IMPACT_UPDATE,
                operations::IMPACT_WITHDRAW,
                operations::IMPACT_CHECKOUT,
                operations::IMPACT_REVIEW
            ]
        );
        // a change that does not exist is left to the operation to report
        must(
            g.allow(&c, &ChangeID::new("nope"), operations::IMPACT_UPDATE)
                .await,
        );
        assert_eq!(a.asked.lock().len(), 4);
    })
    .await;
}
