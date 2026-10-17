//! Port of `baselinediff_test.go`: two baselines are compared node by node: added, removed, changed.

use std::collections::BTreeMap;

use crate::testsupport::*;
use crate::{DIFF_ADDED, DIFF_CHANGED, DIFF_REMOVED, ErrorKind};

#[tokio::test]
async fn diff_baselines() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let res = must(
            land(
                g,
                "",
                &[
                    Landing {
                        properties: props(&[("title", "Use PSP v2".into())]),
                        ..landing("REQ-1", "Requirement")
                    },
                    Landing {
                        properties: props(&[("title", "Obsolete".into())]),
                        ..landing("TST-1", "TestCase")
                    },
                    Landing {
                        properties: props(&[("title", "New".into())]),
                        ..landing("REQ-9", "Requirement")
                    },
                ],
            )
            .await,
        );
        let diff = must(g.diff_baselines(&c, &f.base.id, &res.id).await);
        let got: BTreeMap<String, String> = diff
            .iter()
            .map(|d| (d.key.clone(), d.kind.clone()))
            .collect();
        let want: BTreeMap<String, String> = [
            ("REQ-1", DIFF_CHANGED),
            ("TST-1", DIFF_CHANGED),
            ("REQ-9", DIFF_ADDED),
        ]
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect();
        assert_eq!(got, want);
        let keys: Vec<&str> = diff.iter().map(|d| d.key.as_str()).collect();
        assert_eq!(keys, vec!["REQ-1", "REQ-9", "TST-1"], "ordered by key");
        // backwards, the created node is removed
        for d in must(g.diff_baselines(&c, &res.id, &f.base.id).await) {
            if d.key == "REQ-9" {
                assert_eq!(d.kind, DIFF_REMOVED);
            }
        }
        assert!(must(g.diff_baselines(&c, &res.id, &res.id).await).is_empty());
        // two namespaces are not compared
        let org = must(
            g.branch_head(&c, NAMESPACE_ORGANISATION, goap_domain::MAIN_BRANCH)
                .await,
        );
        assert_kind(
            g.diff_baselines(&c, &org.id, &res.id).await,
            ErrorKind::Invalid,
            "two namespaces",
        );
    })
    .await;
}
