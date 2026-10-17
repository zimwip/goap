//! A change follows a version of its methodology; an upgrade moves it to another (ADR
//! 0113).

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_domain::*;
use goap_graphapi::ChangePatch;
use parking_lot::Mutex;

use crate::tests::attach::Stub;
use crate::tests::lifecycle::new_lifecycle_world;
use crate::testsupport::*;
use crate::{Error, ErrorKind, NewChange};

fn flow(states: &[&str]) -> Lifecycle {
    Lifecycle {
        name: "flow".into(),
        initial: states[0].into(),
        states: states
            .iter()
            .map(|s| LifecycleState {
                name: (*s).into(),
                ..Default::default()
            })
            .collect(),
        ..Default::default()
    }
}

// A change follows the version of its methodology it was created with, a new change the latest published one, a
// sub-change its parent's; an upgrade moves an open change to another version (ADR 0113).
#[tokio::test]
async fn change_methodology_version() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut w = new_lifecycle_world(r).await;
        // versioned: the published versions of methodology m, each with its lifecycle
        let mut of: BTreeMap<String, Lifecycle> = BTreeMap::new();
        of.insert("1".into(), flow(&["open", "review"]));
        of.insert("2".into(), flow(&["open", "review", "done"]));
        of.insert("3".into(), flow(&["start"]));
        of.insert(
            "4".into(),
            Lifecycle {
                name: "other".into(),
                initial: "open".into(),
                states: vec![LifecycleState {
                    name: "open".into(),
                    ..Default::default()
                }],
                ..Default::default()
            },
        );
        let of = Arc::new(of);
        let latest = Arc::new(Mutex::new("1".to_string()));
        let asked: Arc<Mutex<Vec<String>>> = Arc::new(Mutex::new(Vec::new()));
        let (vof, vlatest) = (of.clone(), latest.clone());
        let (lof, llatest, lasked) = (of.clone(), latest.clone(), asked.clone());
        w.g.lifecycles = Some(Arc::new(Stub {
            version: Some(Box::new(move |r| {
                let (name, version) = split_methodology_ref(r);
                if name != "m" {
                    return Ok(String::new());
                }
                if version.is_empty() {
                    return Ok(vlatest.lock().clone());
                }
                if !vof.contains_key(version) {
                    return Err(Error::wrap(
                        ErrorKind::Invalid,
                        format!("no version {version}"),
                    ));
                }
                Ok(version.to_string())
            })),
            lifecycle: Some(Box::new(move |r| {
                lasked.lock().push(r.to_string());
                let (_, version) = split_methodology_ref(r);
                let version = if version.is_empty() {
                    llatest.lock().clone()
                } else {
                    version.to_string()
                };
                lof.get(&version).cloned()
            })),
            ..Default::default()
        }));
        let g = &w.g;
        let root = |title: &str| NewChange {
            project_id: "PROJ-ROOT".into(),
            title: title.into(),
            methodology: "m".into(),
            baseline_id: w.base.id.clone(),
            ..Default::default()
        };

        let old = must(
            g.create_change(
                &ctx,
                NewChange {
                    own_branch: true,
                    ..root("old")
                },
            )
            .await,
        );
        assert!(
            old.methodology_version == "1" && old.methodology_ref() == "m@1",
            "a new change takes the latest version: {:?}",
            old.methodology_version
        );
        assert_eq!(
            asked.lock().last().map(String::as_str),
            Some("m@1"),
            "its lifecycle is the one of that version"
        );

        *latest.lock() = "2".into(); // a new version is published
        let cur = must(g.create_change(&ctx, root("new")).await);
        assert_eq!(
            cur.methodology_version, "2",
            "a change created after the publication takes it"
        );
        assert_eq!(
            must(g.change(&ctx, &old.id).await).methodology_version,
            "1",
            "a change keeps its version"
        );
        let sub = must(
            g.create_change(
                &ctx,
                NewChange {
                    title: "sub".into(),
                    methodology: "m".into(),
                    parent_id: old.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(
            sub.methodology_version, "1",
            "a sub-change of the same methodology follows its parent"
        );
        let pinned = must(
            g.create_change(
                &ctx,
                NewChange {
                    methodology_version: "1".into(),
                    ..root("pinned")
                },
            )
            .await,
        );
        assert_eq!(
            pinned.methodology_version, "1",
            "a change may name its version"
        );
        assert_kind(
            g.create_change(
                &ctx,
                NewChange {
                    methodology_version: "9".into(),
                    ..root("bad")
                },
            )
            .await,
            ErrorKind::Invalid,
            "an unknown version is refused",
        );

        // the upgrade: to the latest, journaled; refused when the change could not stand where it is
        assert_kind(
            g.upgrade_change(&ctx, &old.id, "3").await,
            ErrorKind::Conflict,
            "version 3 has no state open",
        );
        assert_kind(
            g.upgrade_change(&ctx, &old.id, "4").await,
            ErrorKind::Conflict,
            "version 4 follows another lifecycle",
        );
        let up = must(g.upgrade_change(&ctx, &old.id, "").await);
        assert!(
            up.methodology_version == "2" && up.state == "open",
            "upgraded to the latest: {up:?}"
        );
        let (entries, _) = must(
            g.change_log(
                &ctx,
                &LogFilter {
                    change: old.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        let found = entries.iter().any(|e| {
            e.r#type == format!("{LOG_CHANGE}.updated")
                && serde_json::from_value::<HeaderEdit>(e.payload.clone()).is_ok_and(|h| {
                    h.fields
                        .get(crate::HEADER_METHODOLOGY_VERSION)
                        .is_some_and(|hv| hv.from == "1" && hv.to == "2")
                })
        });
        assert!(found, "the upgrade is journaled: {entries:?}");
        let again = must(g.upgrade_change(&ctx, &old.id, "2").await);
        assert_eq!(
            again.methodology_version, "2",
            "upgrading to the version it follows changes nothing"
        );
        assert_eq!(
            must(g.change(&ctx, &sub.id).await).methodology_version,
            "1",
            "its sub-change keeps its own"
        );
        must(
            g.update_change(
                &ctx,
                &cur.id,
                &ChangePatch {
                    status: Some(CHANGE_ABANDONED),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_kind(
            g.upgrade_change(&ctx, &cur.id, "1").await,
            ErrorKind::Conflict,
            "a closed change keeps its version",
        );
    })
    .await;
}
