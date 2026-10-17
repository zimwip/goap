//! Port of `attach_test.go`: a root change attached to a change whose methodology integrates its own, detached again
//! (ADR 0106). Also holds [`Stub`], the configurable ChangeLifecycles of the tests of the guardian chain, of the move
//! and of the versions.

use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::*;
use parking_lot::Mutex;
use serde_json::json;

use crate::tests::helpers::*;
use crate::tests::subchange::{new_org_world, root_change};
use crate::testsupport::*;
use crate::{ChangeLifecycles, ErrorKind, NewChange, Result};

type GuardiansFn = dyn Fn(&str, &str) -> Vec<GuardianRef> + Send + Sync;
type CheckFn = dyn Fn(&Blackboard, &[Change]) -> String + Send + Sync;
type VersionFn = dyn Fn(&str) -> Result<String> + Send + Sync;
type LifecycleFn = dyn Fn(&str) -> Option<Lifecycle> + Send + Sync;
type IntegratesFn = dyn Fn(&str, &str) -> bool + Send + Sync;

/// Stub is a ChangeLifecycles whose say the test gives as functions; the others answer like an empty registry
/// (Go embeds a stub type and overrides some methods).
#[derive(Default)]
pub(crate) struct Stub {
    pub(crate) guardians: Option<Box<GuardiansFn>>,
    pub(crate) integrates: Option<Box<IntegratesFn>>,
    pub(crate) check: Option<Box<CheckFn>>,
    pub(crate) version: Option<Box<VersionFn>>,
    pub(crate) lifecycle: Option<Box<LifecycleFn>>,
}

#[async_trait]
impl ChangeLifecycles for Stub {
    async fn methodology_version(&self, _: &Ctx, m: &str) -> Result<String> {
        match &self.version {
            Some(f) => f(m),
            None => Ok(String::new()),
        }
    }
    async fn lifecycle(&self, _: &Ctx, m: &str) -> Result<Option<Lifecycle>> {
        Ok(self.lifecycle.as_ref().and_then(|f| f(m)))
    }
    async fn default_goal(&self, _: &Ctx, _: &str) -> Result<String> {
        Ok(String::new())
    }
    async fn guard(&self, _: &Ctx, _: &Blackboard, _: &str, _: &str, _: &str) -> Result<bool> {
        Ok(true)
    }
    async fn gate(&self, _: &Ctx, _: &Blackboard, _: &Transition, _: &str) -> Result<GateResult> {
        Ok(GateResult::default())
    }
    async fn merge(&self, _: &Ctx, _: &Change, _: &Blackboard) -> Result<(bool, bool)> {
        Ok((false, false))
    }
    async fn guardians(&self, _: &Ctx, m: &str, project: &str) -> Result<Vec<GuardianRef>> {
        Ok(match &self.guardians {
            Some(f) => f(m, project),
            None => vec![guardian(m)],
        })
    }
    async fn integrates(&self, _: &Ctx, integrator: &str, m: &str) -> Result<bool> {
        Ok(self.integrates.as_ref().is_some_and(|f| f(integrator, m)))
    }
    async fn check_guardians(
        &self,
        _: &Ctx,
        bb: &Blackboard,
        _: &str,
        ancestors: &[Change],
    ) -> Result<String> {
        Ok(self
            .check
            .as_ref()
            .map(|f| f(bb, ancestors))
            .unwrap_or_default())
    }
}

pub(crate) fn guardian(m: &str) -> GuardianRef {
    GuardianRef {
        methodology: m.into(),
        ..Default::default()
    }
}

/// The methodologies of a guardian chain.
pub(crate) fn names(c: &Change) -> Vec<String> {
    c.guardians.iter().map(|r| r.methodology.clone()).collect()
}

/// chainGate is a guardian chain of two methodologies (ADR 0106): "release" integrates "sdlc", exclusively (a change
/// of sdlc merges only once attached to a release); it records the changes it was asked about.
fn chain_gate(asked: Arc<Mutex<Vec<ChangeID>>>) -> Stub {
    Stub {
        guardians: Some(Box::new(|m, _| {
            if m == "sdlc" {
                vec![guardian("release"), guardian("sdlc")]
            } else {
                vec![guardian(m)]
            }
        })),
        integrates: Some(Box::new(|i, m| i == "release" && m == "sdlc")),
        check: Some(Box::new(move |bb, ancestors| {
            asked.lock().push(bb.change.id.clone());
            if ancestors.first().is_none_or(|a| a.methodology != "release") {
                "release refuses: integrator.attached".into()
            } else {
                String::new()
            }
        })),
        ..Default::default()
    }
}

#[tokio::test]
async fn attach_change() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut w = new_org_world(r).await;
        let asked = Arc::new(Mutex::new(Vec::new()));
        w.g.lifecycles = Some(Arc::new(chain_gate(asked.clone())));
        let g = &w.g;
        let of = |title: &str, m: &str| NewChange {
            methodology: m.into(),
            ..root_change(&w.base, title)
        };
        let release = must(g.create_change(&ctx, of("R1", "release")).await);
        let cand = must(g.create_change(&ctx, of("fix", "sdlc")).await);
        assert_eq!(
            names(&cand),
            ["release", "sdlc"],
            "a root change of sdlc gets the chain of its project"
        );
        assert_eq!(
            names(&must(g.change(&ctx, &cand.id).await)),
            ["release", "sdlc"],
            "the chain is stored on the change"
        );

        let added = must(
            g.propose_impact(
                &ctx,
                &cand.id,
                vec![ChangeImpact {
                    intent: INTENT_MODIFIED,
                    pre: Some(w.cmp3.r#ref()),
                    rationale: "fix".into(),
                    ..Default::default()
                }],
            )
            .await,
        );
        must(
            edit(
                g,
                &cand.id,
                &added[0].id,
                Edit {
                    properties: props(&[("title", json!("fixed"))]),
                    ..Default::default()
                },
            )
            .await,
        );
        must(accept(g, &cand.id, &added[0].id, "u", "ok").await);
        must(commit_change(g, &cand.id, "").await);
        // the outer guardian holds the merge of a change not attached to a release
        assert_kind(
            integrate_change(g, &cand.id).await,
            ErrorKind::Conflict,
            "an exclusive integrator refuses the merge of an unattached change",
        );

        // a change of a methodology the parent does not integrate is not attached
        let other = must(g.create_change(&ctx, of("other", "release")).await);
        assert_kind(
            g.attach_change(&ctx, &other.id, &release.id).await,
            ErrorKind::Invalid,
            "attach outside integrates",
        );
        assert_kind(
            g.attach_change(&ctx, &cand.id, &cand.id).await,
            ErrorKind::Invalid,
            "attach to itself",
        );

        let att = must(g.attach_change(&ctx, &cand.id, &release.id).await);
        assert!(
            att.parent_id == release.id && names(&att) == ["release", "sdlc"],
            "attached: parent {}, chain {:?}",
            att.parent_id,
            names(&att)
        );
        let rel = must(g.change(&ctx, &release.id).await);
        let b = must(g.branch(&ctx, &att.namespace, &att.branch).await);
        assert_eq!(
            b.parent, rel.branch,
            "the attached change forks from the branch of its parent"
        );
        assert_eq!(
            crate::tests::subchange::title_in(g, &cand.id, "CMP-3").await,
            "fixed",
            "what the change committed is rebased onto its parent"
        );
        assert_kind(
            g.attach_change(&ctx, &cand.id, &release.id).await,
            ErrorKind::Conflict,
            "attached twice",
        );

        // detached, it is a root change again, its chain recomputed
        let det = must(g.detach_change(&ctx, &cand.id).await);
        assert!(
            det.parent_id.is_empty() && names(&det) == ["release", "sdlc"],
            "detached: parent {}, chain {:?}",
            det.parent_id,
            names(&det)
        );
        assert_kind(
            g.detach_change(&ctx, &cand.id).await,
            ErrorKind::Invalid,
            "detach a root change",
        );

        // attached again, it merges into the release, which holds its work
        must(g.attach_change(&ctx, &cand.id, &release.id).await);
        must(integrate_change(g, &cand.id).await);
        assert_eq!(
            crate::tests::subchange::title_in(g, &release.id, "CMP-3").await,
            "fixed",
            "the release holds the work of its candidate"
        );
        let n = must(g.node_by_key(&ctx, DEFAULT_NAMESPACE, "CMP-3").await);
        assert_ne!(
            n.properties["title"], "fixed",
            "main moved before the release merged"
        );
        assert!(
            asked.lock().contains(&cand.id),
            "the outer guardians were asked: {:?}",
            asked.lock()
        );
    })
    .await;
}
